package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Config holds the application configuration
type Config struct {
	SerialPort string `json:"serial_port"`
	BaudRate   int    `json:"baud_rate"`
	MQTTBroker string `json:"mqtt_broker"`
	MQTTUser   string `json:"mqtt_user"`
	MQTTPass   string `json:"mqtt_pass"`
	LogLevel   string `json:"log_level"`
	GatewayID  string `json:"gateway_id"`

	// Transmission Manager Config
	DutyCycleMinCredits int    `json:"duty_cycle_min_credits"`
	CommandTimeout      string `json:"command_timeout"`
	MaxCulQueue         int    `json:"max_cul_queue"`
	TxCheckPeriod       string `json:"tx_check_period"`
}

// Global state
var (
	config       Config
	mqttClient   mqtt.Client
	serialWrite  chan string
	pairingUntil time.Time

	// Message Counter
	msgCounter      int
	msgCounterMutex sync.Mutex

	// Device State Cache (safe for concurrent access)
	stateCache = make(map[string]*MaxDeviceData)
	stateMutex sync.RWMutex

	// Device Config Cache - stores configuration values for each device
	deviceConfigCache = make(map[string]*DeviceConfig)
	configMutex       sync.RWMutex
)

// Constants for MAX! Protocol
const (
	MaxMsgStart      = 'Z'
	TopicFormatState = "max/%s/state"
	TopicFormatValve = "max/%s/valve"
	HexTypeFormat    = "0x%02X"
)

func main() {
	// 1. Load Configuration
	loadConfig()
	setupLogging()

	fmt.Printf("time=\"%s\" level=info msg=\"Starting MAX! to MQTT Bridge\"\n", time.Now().Format(time.RFC3339))

	// 1.5. Duty Cycle + Ack tracker (push confirmation, no poll)
	initTransmissionManager()
	initAckTracker()

	// 2. Setup MQTT
	setupMQTT()

	// 3. Setup Serial & Main Loop
	serialChan := make(chan string, 10)
	serialWrite = make(chan string, 10)
	go serialReaderLoop(serialChan, serialWrite)

	// Start periodic time sync (broadcasts time on startup and every 24 hours)
	go startPeriodicTimeSync()
	go startCreditPolling()

	// 4. Handle Signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 5. Processing Loop
	for {
		select {
		case msg := <-serialChan:
			handleSerialMessage(msg)
		case <-sigChan:
			slog.Warn("Shutting down...")
			setCULOnline(false)
			return
		}
	}
}

func loadConfig() {
	// Defaults
	config = Config{
		SerialPort:          "/dev/serial/by-id/usb-SHK_NANO_CUL_868-if00-port0",
		BaudRate:            38400,
		MQTTBroker:          "tcp://homeassistant:1883",
		LogLevel:            "info",
		GatewayID:           "123456",
		DutyCycleMinCredits: 200,
		CommandTimeout:      "1m",
		MaxCulQueue:         5,
		TxCheckPeriod:       "1m",
	}

	// Try loading from options.json (Standard HA Add-on location)
	loadConfigFromFile()

	// Override with Env Vars (Standard Docker pattern)
	loadEnvOverrides()
}

// loadConfigFromFile loads config from HA Add-on options.json if present
func loadConfigFromFile() {
	if _, err := os.Stat("/data/options.json"); err != nil {
		return
	}
	data, err := os.ReadFile("/data/options.json")
	if err != nil {
		return
	}
	slog.Info("Loading config from /data/options.json")
	_ = json.Unmarshal(data, &config)
}

// loadEnvOverrides applies environment variable overrides to config
// loadEnvOverrides applies environment variable overrides to config
func loadEnvOverrides() {
	loadEnvString("SERIAL_PORT", &config.SerialPort)
	loadEnvInt("DUTY_CYCLE_MIN_CREDITS", &config.DutyCycleMinCredits)
	loadEnvString("COMMAND_TIMEOUT", &config.CommandTimeout)
	loadEnvInt("MAX_CUL_QUEUE", &config.MaxCulQueue)
	loadEnvString("TX_CHECK_PERIOD", &config.TxCheckPeriod)
	loadEnvString("MQTT_BROKER", &config.MQTTBroker)
	loadEnvString("MQTT_USER", &config.MQTTUser)
	loadEnvString("MQTT_PASS", &config.MQTTPass)
	loadEnvString("GATEWAY_ID", &config.GatewayID)
	loadEnvString("LOG_LEVEL", &config.LogLevel)
	loadEnvInt("BAUD_RATE", &config.BaudRate)
}

func loadEnvString(key string, target *string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

func loadEnvInt(key string, target *int) {
	if v := os.Getenv(key); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			*target = val
		}
	}
}

func setupLogging() {
	var lvl slog.Level
	switch strings.ToLower(config.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: lvl,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)

	slog.Info("Log level set", "level", config.LogLevel)
	slog.Debug("Config snapshot",
		"serial_port", config.SerialPort,
		"gateway_id", config.GatewayID,
		"mqtt_broker", config.MQTTBroker,
		"duty_cycle_min_credits", config.DutyCycleMinCredits,
		"command_timeout", config.CommandTimeout,
		"max_cul_queue", config.MaxCulQueue,
		"tx_check_period", config.TxCheckPeriod,
	)
}

func setupMQTT() {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(config.MQTTBroker)
	if config.MQTTUser != "" {
		opts.SetUsername(config.MQTTUser)
		opts.SetPassword(config.MQTTPass)
	}
	opts.SetClientID("max2mqtt_bridge")
	opts.SetAutoReconnect(true)
	// Entities become unavailable when the bridge loses its MQTT connection
	opts.SetWill(bridgeStatusTopic, payloadOffline, 1, true)
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		slog.Info("Connected to MQTT Broker")
		c.Subscribe("max/+/set", 0, handleMQTTCommand)
		c.Subscribe("max/+/mode/set", 0, handleMQTTModeCommand)
		c.Subscribe("max/+/preset/set", 0, handleMQTTPresetCommand)
		c.Subscribe("max/+/vacation/set", 0, handleMQTTVacation)
		c.Subscribe("max/bridge/pair", 0, handleMQTTPair)
		c.Subscribe("max/+/eco_temp/set", 0, handleMQTTEcoTemp)
		c.Subscribe("max/+/comfort_temp/set", 0, handleMQTTComfortTemp)
		c.Subscribe("max/+/max_temp/set", 0, handleMQTTMaxTemp)
		c.Subscribe("max/+/min_temp/set", 0, handleMQTTMinTemp)
		c.Subscribe("max/+/measurement_offset/set", 0, handleMQTTMeasurementOffset)
		c.Subscribe("max/+/window_open_temp/set", 0, handleMQTTWindowOpenTemp)
		c.Subscribe("max/+/window_open_duration/set", 0, handleMQTTWindowOpenDuration)
		c.Subscribe("max/+/display_mode/set", 0, handleMQTTDisplayMode)
		c.Subscribe("max/+/group_id/set", 0, handleMQTTGroupID)
		c.Subscribe("max/+/boost_duration/set", 0, handleMQTTBoostDuration)
		c.Subscribe("max/+/boost_valve_position/set", 0, handleMQTTBoostValvePosition)
		c.Subscribe("max/+/decalc_day/set", 0, handleMQTTDecalcDay)
		c.Subscribe("max/+/decalc_hour/set", 0, handleMQTTDecalcHour)
		c.Subscribe("max/+/max_valve_setting/set", 0, handleMQTTMaxValveSetting)
		c.Subscribe("max/+/valve_offset/set", 0, handleMQTTValveOffset)
		c.Subscribe("max/+/associate", 0, handleMQTTAssociate)
		c.Subscribe("max/+/disassociate/set", 0, handleMQTTDisassociate)
		c.Subscribe("max/+/factory_reset/set", 0, handleMQTTFactoryReset)
		c.Subscribe("max/+/week_profile/set", 0, handleMQTTWeekProfile)
		c.Subscribe("max/+/config/import", 0, handleMQTTConfigImport)
		c.Subscribe("max/+/config/set", 0, handleMQTTConfigSet)
		publishBridgeDiscovery()
		publishBridgeStatus()
	})

	mqttClient = mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		slog.Warn("Could not connect to MQTT initially, will retry in background", "error", token.Error())
	}
}

// Serial Reader Loop with Reconnection
func serialReaderLoop(out chan<- string, in <-chan string) {
	for {
		port := openCulConn()
		if port == nil {
			time.Sleep(5 * time.Second)
			continue
		}

		initializeCUL(port)
		setCULOnline(true)

		// Start Writer Routine for this connection
		stopWriter := make(chan struct{})
		go startSerialWriter(port, in, stopWriter)

		// Read loop
		scanner := bufio.NewScanner(port)
		for scanner.Scan() {
			processSerialLine(scanner.Text(), out)
		}

		close(stopWriter)
		setCULOnline(false)
		slog.Warn("CUL connection lost or closed. Reconnecting...")
		port.Close()
		time.Sleep(2 * time.Second)
	}
}

// initializeCUL sends initialization commands to the CUL device
func initializeCUL(port CulConn) {
	// Allow reliable device startup/reboot time (Increased to 4s for NanoCULs)
	time.Sleep(4 * time.Second)

	// Clear any garbage from boot process
	port.ResetInputBuffer()

	// Send Initialization Commands
	// V   = Version Check (Tests comms)
	// Zr  = Enable MAX! Sniffer Mode (Critical)
	// Za  = Set auto-ack address: culfw acknowledges packets sent to the gateway (as FHEM CUL_MAX does).
	//       Without it, paired devices repeat their messages and set the rferror bit.
	// X   = Query Credits
	initCmds := []string{"V", "Zr", "Za" + gatewayAddrHex(), "X"}
	for _, cmd := range initCmds {
		slog.Info("Sending Init Command", "command", cmd)
		if _, err := port.Write([]byte(cmd + "\r")); err != nil {
			slog.Error("Failed to send init command", "command", cmd, "error", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// startSerialWriter runs the serial write loop until stopWriter is closed
func startSerialWriter(port CulConn, in <-chan string, stopWriter <-chan struct{}) {
	for {
		select {
		case cmd := <-in:
			// Credit polls ("X") are frequent; keep them at debug. Zs/init stay at info.
			if cmd == "X" {
				slog.Debug("CUL TX", "command", cmd)
			} else {
				slog.Info("CUL TX", "command", cmd)
			}
			if _, err := port.Write([]byte(cmd + "\r")); err != nil {
				slog.Error("Failed to write to CUL", "error", err)
				return
			}
		case <-stopWriter:
			return
		}
	}
}

// processSerialLine handles a single line received from the serial port
func processSerialLine(text string, out chan<- string) {
	slog.Debug("RX", "text", text)

	if strings.HasPrefix(text, string(MaxMsgStart)) {
		out <- text
		return
	}

	if text == "LOVF" {
		slog.Warn("CUL: LOVF (Limit Of Voice Full) - TX buffer overflow. Recommendation: Increase duty_cycle_min_credits")
		return
	}

	// Check for Credit Report: "yy xxx" (e.g., "00 900")
	if txMgr != nil {
		queueLen, credits, matched := ParseCreditResponse(text)
		if matched {
			slog.Debug("CUL credit report", "credits", credits, "cul_queue", queueLen, "raw", text)
			txMgr.UpdateCredits(credits, queueLen)
			publishCredits(credits, queueLen)
			txMgr.SignalCreditResponse()
			return
		}
	}

	if text != "" {
		slog.Debug("CUL non-MAX line", "text", text)
	}
}

// Parser Logic
type MaxPacket struct {
	Raw      string
	Length   int
	MsgCount int
	Flag     int
	Type     int
	SrcAddr  string
	DstAddr  string
	Group    int
	Payload  []byte
}

func parseMaxMessage(raw string) (*MaxPacket, error) {
	// Raw: Z0B0102031234561234560012
	// Remove 'Z'
	if len(raw) < 2 {
		return nil, fmt.Errorf("message too short")
	}
	hexStr := raw[1:]

	// Decode Hex
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %v", err)
	}

	if len(data) < 10 { // Min header size
		return nil, fmt.Errorf("data too short for header")
	}

	// Structure (Z + Hex)
	// Byte 0: Length
	// Byte 1: Cnt
	// Byte 2: Flg
	// Byte 3: Type
	// Byte 4-6: Src
	// Byte 7-9: Dst
	// Byte 10: Grp
	// Byte 11+: Payload

	if len(data) < 11 { // Min header size
		return nil, fmt.Errorf("data too short for header")
	}

	pkt := &MaxPacket{
		Raw:      raw,
		Length:   int(data[0]),
		MsgCount: int(data[1]),
		Flag:     int(data[2]),
		Type:     int(data[3]),
		SrcAddr:  fmt.Sprintf("%06X", data[4:7]),
		DstAddr:  fmt.Sprintf("%06X", data[7:10]),
		Group:    int(data[10]),
		Payload:  data[11:],
	}

	return pkt, nil
}

func handleSerialMessage(raw string) {
	raw = strings.TrimSpace(raw)
	pkt, err := parseMaxMessage(raw)
	if err != nil {
		slog.Debug("Failed to parse message", "raw", raw, "error", err)
		return
	}

	slog.Info("Parsed Packet", "src", pkt.SrcAddr, "type", fmt.Sprintf(HexTypeFormat, pkt.Type), "payload", fmt.Sprintf("%X", pkt.Payload))
	slog.Debug("Parsed Packet detail",
		"src", pkt.SrcAddr,
		"dst", pkt.DstAddr,
		"type", fmt.Sprintf(HexTypeFormat, pkt.Type),
		"flag", fmt.Sprintf("0x%02X", pkt.Flag),
		"group", pkt.Group,
		"cnt", pkt.MsgCount,
		"len", pkt.Length,
		"payload", fmt.Sprintf("%X", pkt.Payload),
		"raw", pkt.Raw,
	)

	// Check if this is a PairPing (Type 0x00) and we are in pairing mode
	if pkt.Type == 0x00 {
		if time.Now().Before(pairingUntil) {
			slog.Info("Received PairPing from while in pairing mode. Sending PairPong...", "src", pkt.SrcAddr)
			sendPairPong(pkt.SrcAddr)
		} else {
			slog.Debug("Received PairPing but pairing mode is not active.", "src", pkt.SrcAddr)
		}
		return
	}

	// Check if this is a TimeInformation request (Type 0x03)
	// Devices send this to request current time from the gateway
	if pkt.Type == 0x03 {
		slog.Info("Received TimeInformation request, sending time...", "src", pkt.SrcAddr)
		sendTimeToDevice(pkt.SrcAddr)
		return
	}

	// ShutterContactState (0x30): window/door contact, no climate entity
	if pkt.Type == 0x30 {
		touchLastSeen(pkt.SrcAddr)
		handleShutterContact(pkt)
		return
	}

	// Message Type Reference (from FHEM 10_MAX.pm):
	// 0x00 = PairPing
	// 0x02 = Ack (contains state data)
	// 0x03 = TimeInformation (request from device or broadcast from gateway)
	// 0x60 = ThermostatState (HeatingThermostat) - IGNORED
	// 0x70 = WallThermostatState (WallMountedThermostat)
	// 0x42 = WallThermostatControl
	// 0x30 = ShutterContactState
	// 0x40 = SetTemperature

	// Filter: Process Wall Thermostats (0x70), Radiator Thermostats (0x60), and Control/Acks.
	// 1. Explicit State types: 0x70 (Wall), 0x60 (Radiator), 0x42 (Wall Control)
	// 2. Ack (0x02):
	//    - Extended: Length >= 6 (Has actual temp)
	//    - Standard: Length >= 4 (Has Setpoint/Mode/Valve) - Essential for confirmation
	// 3. SetTemperature (0x40): Manual change on Wall Thermostat
	touchLastSeen(pkt.SrcAddr)

	// Acks sent by the CUL itself (auto-ack, see initializeCUL) are not device Acks
	if pkt.Type == 0x02 && pkt.SrcAddr == gatewayAddrHex() {
		slog.Debug("Ignored own auto-Ack", "dst", pkt.DstAddr)
		return
	}

	// Any Ack (including short) confirms pending config / setpoint TX via CUL push — no polling
	if pkt.Type == 0x02 {
		if ackTracker != nil {
			ackTracker.Confirm(pkt.SrcAddr)
			// Short Acks have no temp payload; still apply expected setpoint (FHEM AckSetTemperature)
			if len(pkt.Payload) < 4 {
				if ackTracker.ApplySetpointAck(pkt.SrcAddr) {
					slog.Info("Short Ack applied pending setpoint", "src", pkt.SrcAddr)
					return
				}
				slog.Debug("Short Ack (no pending setpoint)", "src", pkt.SrcAddr, "dst", pkt.DstAddr, "payload_len", len(pkt.Payload))
				return
			}
			ackTracker.ApplySetpointAck(pkt.SrcAddr)
		}
	}

	isValidType := pkt.Type == 0x70 || pkt.Type == 0x60 || pkt.Type == 0x42 || pkt.Type == 0x40
	isValidAck := pkt.Type == 0x02 && len(pkt.Payload) >= 4

	if !isValidType && !isValidAck {
		slog.Debug("Ignored message from device", "src", pkt.SrcAddr, "dst", pkt.DstAddr, "type", fmt.Sprintf(HexTypeFormat, pkt.Type), "group", pkt.Group, "payload_len", len(pkt.Payload))
		return
	}

	markDeviceType(pkt.SrcAddr, pkt.Type)
	// Remember room/group from RX — SetTemperature requires matching groupId + flag 0x04 (FHEM/Cube)
	if pkt.Group > 0 {
		updateDeviceGroupID(pkt.SrcAddr, pkt.Group)
	} else {
		slog.Debug("RX group is 0 (SetTemp will use flag 0x00)", "src", pkt.SrcAddr, "type", fmt.Sprintf(HexTypeFormat, pkt.Type))
	}

	newData := decodePayload(pkt)
	if newData == nil {
		slog.Debug("decodePayload returned nil", "src", pkt.SrcAddr, "type", fmt.Sprintf(HexTypeFormat, pkt.Type), "payload_len", len(pkt.Payload))
		return
	}

	sendDiscovery(pkt.SrcAddr)
	updateDeviceState(pkt.SrcAddr, newData)
	if newData.ValvePosition != nil {
		publishValvePosition(pkt.SrcAddr, *newData.ValvePosition)
	}
}

func updateDeviceState(srcAddr string, newData *MaxDeviceData) {
	stateMutex.Lock()
	existing, exists := stateCache[srcAddr]
	if !exists {
		existing = &MaxDeviceData{
			Mode:     "manual",
			HVACMode: "heat",
		}
		stateCache[srcAddr] = existing
	}

	// Merge specific fields if valid
	if newData.Temperature > 0 {
		existing.Temperature = newData.Temperature
	}
	if newData.CurrentTemperature != nil {
		existing.CurrentTemperature = newData.CurrentTemperature
	}

	if newData.Mode != "" {
		existing.Mode = newData.Mode
		if newData.Mode != "vacation" {
			existing.Until = ""
		}
	}
	if newData.Until != "" {
		existing.Until = newData.Until
	}
	if newData.HVACMode != "" {
		existing.HVACMode = newData.HVACMode
	}
	if newData.Battery != "" {
		existing.Battery = newData.Battery
	}
	if newData.RFError != nil {
		existing.RFError = newData.RFError
	}
	if newData.PresetMode != "" {
		existing.PresetMode = newData.PresetMode
	} else if newData.Mode != "" {
		// A status message carries the device's real mode: derive the preset from it,
		// so a boost that ended on the device (or an eco/comfort setpoint changed there) is not kept
		existing.PresetMode = presetFromStatus(srcAddr, existing.PresetMode, existing.Mode, existing.Temperature)
	} else if existing.PresetMode == "" {
		existing.PresetMode = "none"
	}

	// Check for pending verification
	if existing.Temperature > 0 && verificationMgr != nil {
		verificationMgr.CheckVerification(srcAddr, existing.Temperature)
	}

	// Calculate HVAC Action based on User Rule:
	// if current_temperature < temperature, then "heating" else "idle"
	if existing.CurrentTemperature != nil {
		if *existing.CurrentTemperature < existing.Temperature {
			existing.HVACAction = "heating"
		} else {
			existing.HVACAction = "idle"
		}
	} else {
		// Without current temp, we default to idle to be safe,
		// or we could leave it explicitly empty/unknown.
		existing.HVACAction = "idle"
	}

	// Make a copy for publishing to avoid race conditions after unlock
	dataToPublish := *existing
	if existing.CurrentTemperature != nil {
		val := *existing.CurrentTemperature
		dataToPublish.CurrentTemperature = &val
	}
	stateMutex.Unlock()

	slog.Info("Decoded Data", "src", srcAddr, "data", &dataToPublish)
	publishState(srcAddr, &dataToPublish)
}

// presetFromStatus returns the preset for a received status: boost while the device is in
// boost mode; eco/comfort only while the device stays in manual mode at that temperature; else none
func presetFromStatus(srcAddr, prev, mode string, temp float64) string {
	if mode == "boost" {
		return "boost"
	}
	if mode == "manual" && (prev == "eco" || prev == "comfort") {
		cfg := getDeviceConfig(srcAddr)
		configMutex.RLock()
		eco, comfort := cfg.EcoTemp, cfg.ComfortTemp
		configMutex.RUnlock()
		if (prev == "eco" && temp == eco) || (prev == "comfort" && temp == comfort) {
			return prev
		}
	}
	return "none"
}

type MaxDeviceData struct {
	Temperature        float64  `json:"temperature"`
	CurrentTemperature *float64 `json:"current_temperature"`

	Mode       string `json:"mode"`
	HVACMode   string `json:"hvac_mode"`
	HVACAction string `json:"hvac_action"`
	Battery    string `json:"battery"`
	PresetMode string `json:"preset_mode"`
	// RFError: device reports it cannot reach its link partner / gateway (status bit 6)
	RFError *bool `json:"rf_error,omitempty"`

	// ValvePosition (0-100 %) from radiator thermostats; published on its own retained topic,
	// so the last value survives a bridge restart (state cache starts empty)
	ValvePosition *int `json:"-"`

	// Until: end of vacation mode as "YYYY-MM-DD HH:MM" (device local time), only in vacation mode
	Until string `json:"until,omitempty"`
}

// convert pointer to string for appropriate logging
func (d *MaxDeviceData) String() string {
	curTempStr := "N/A"
	if d.CurrentTemperature != nil {
		curTempStr = fmt.Sprintf("%.1f", *d.CurrentTemperature)
	}
	valveStr := "N/A"
	if d.ValvePosition != nil {
		valveStr = fmt.Sprintf("%d%%", *d.ValvePosition)
	}
	return fmt.Sprintf("Temp: %.1f, CurTemp: %s, Mode: %s, Action: %s, Batt: %s, Valve: %s",
		d.Temperature, curTempStr, d.Mode, d.HVACAction, d.Battery, valveStr)
}

// decodePayload parses the MAX! thermostat state payload
// Based on references/protocol
func decodePayload(pkt *MaxPacket) *MaxDeviceData {
	switch pkt.Type {
	case 0x42:
		return decodeWallThermostatControl(pkt.Payload)
	case 0x40:
		return decodeSetTemperature(pkt.Payload)
	case 0x02, 0x60, 0x70:
		return decodeThermostatStatus(pkt.Type, pkt.Payload, isRadiatorDevice(pkt.SrcAddr))
	default:
		return nil
	}
}

// decodeWallThermostatControl decodes Type 0x42 (WallThermostatControl)
// Payload: [SetpointRaw] [ActualTempRaw]
func decodeWallThermostatControl(payload []byte) *MaxDeviceData {
	if len(payload) < 2 {
		return nil
	}
	data := &MaxDeviceData{}

	// Byte 0: Setpoint (Bits 0-6) / 2
	data.Temperature = float64(payload[0]&0x7F) / 2.0

	// Byte 1: Actual Temperature (Raw / 10)
	actual := float64(payload[1]) / 10.0
	data.CurrentTemperature = &actual

	return data
}

// decodeSetTemperature decodes Type 0x40 (SetTemperature)
// Payload: [TargetTemp/Mode] - Bits 0-5: Temp/2, Bits 6-7: Mode
func decodeSetTemperature(payload []byte) *MaxDeviceData {
	if len(payload) < 1 {
		return nil
	}
	data := &MaxDeviceData{}
	byte0 := payload[0]

	// Mode from bits 6-7
	modeBits := (byte0 >> 6) & 0x03
	applyModeFromBits(data, modeBits)

	// Temperature from bits 0-5
	data.Temperature = float64(byte0&0x3F) / 2.0

	// Bytes 1-3: vacation end date/time
	if modeBits == 0x02 && len(payload) >= 4 {
		data.Until = decodeUntil(payload[1:4])
	}

	// Special case: Off (4.5C in manual mode)
	if data.Mode == "manual" && data.Temperature <= 4.5 {
		data.HVACMode = "off"
	}

	return data
}

// decodeThermostatStatus decodes Type 0x02, 0x60, 0x70 (Thermostat Status)
// Contains Mode, Battery, RF error, Setpoint, and optionally Actual Temperature.
// Byte 1 is the valve position for radiator thermostats (0x60, and their Acks);
// for wall thermostats (0x70) it is the display flag, so isRadiator gates it for Acks.
func decodeThermostatStatus(pktType int, payload []byte, isRadiator bool) *MaxDeviceData {
	// For Type 0x02, skip the first byte (generic status/ack)
	if pktType == 0x02 {
		if len(payload) < 1 {
			return nil
		}
		payload = payload[1:]
	}

	if len(payload) < 3 {
		return nil
	}

	data := &MaxDeviceData{}

	// Byte 0: Mode (bits 0-1) & Battery (bit 7)
	modeBits := payload[0] & 0x03
	applyModeFromBits(data, modeBits)

	if (payload[0] & 0x80) != 0 {
		data.Battery = "low"
	} else {
		data.Battery = "ok"
	}

	// Byte 0 bit 6: RF error (link partner / gateway not reachable)
	rfError := (payload[0] & 0x40) != 0
	data.RFError = &rfError

	// Byte 1: Valve position in % (radiator thermostats only)
	if pktType == 0x60 || (pktType == 0x02 && isRadiator) {
		if valve := int(payload[1]); valve <= 100 {
			data.ValvePosition = &valve
		}
	}

	// Byte 2: Setpoint Temperature
	data.Temperature = float64(payload[2]&0x7F) / 2.0

	// Special case: Off (4.5C in manual mode)
	if data.Mode == "manual" && data.Temperature <= 4.5 {
		data.HVACMode = "off"
	}

	// Bytes 3-5: vacation end date/time in vacation mode, otherwise
	// Bytes 3-4: Actual Temperature (if present).
	// Radiator thermostats send 0 when they have no new measurement, and very rarely
	// values like 0.2 °C; FHEM ignores both, so keep the last known temperature.
	if modeBits == 0x02 {
		if len(payload) >= 6 {
			data.Until = decodeUntil(payload[3:6])
		}
	} else if len(payload) >= 5 {
		rawTemp := int(payload[3]&0x01)<<8 | int(payload[4])
		actualTemp := float64(rawTemp) / 10.0
		if actualTemp >= 1 {
			data.CurrentTemperature = &actualTemp
		}
	}

	return data
}

// applyModeFromBits sets Mode and HVACMode based on mode bits
// Mode bits: 00=Auto, 01=Manual, 10=Vacation, 11=Boost
func applyModeFromBits(data *MaxDeviceData, modeBits byte) {
	switch modeBits {
	case 0:
		data.Mode = "auto"
		data.HVACMode = "auto"
	case 1:
		data.Mode = "manual"
		data.HVACMode = "heat"
	case 2:
		data.Mode = "vacation"
		data.HVACMode = "off"
	case 3:
		data.Mode = "boost"
		data.HVACMode = "heat"
	}
}

func sendDiscovery(srcAddr string) {
	deviceID := "max_" + srcAddr
	deviceInfo := discoveryDeviceInfo(srcAddr)

	climateTopic := fmt.Sprintf("homeassistant/climate/%s/config", deviceID)
	climatePayload := map[string]interface{}{
		"name":                         "MAX! Thermostat " + srcAddr,
		"unique_id":                    deviceID + "_climate",
		"state_topic":                  fmt.Sprintf(TopicFormatState, srcAddr),
		"temperature_command_topic":    fmt.Sprintf("max/%s/set", srcAddr),
		"temperature_state_topic":      fmt.Sprintf(TopicFormatState, srcAddr),
		"temperature_state_template":   "{{ value_json.temperature }}",
		"current_temperature_topic":    fmt.Sprintf(TopicFormatState, srcAddr),
		"current_temperature_template": "{{ value_json.get('current_temperature') }}",
		"mode_state_topic":             fmt.Sprintf(TopicFormatState, srcAddr),
		"mode_state_template":          "{{ value_json.get('hvac_mode', 'heat') }}",
		"mode_command_topic":           fmt.Sprintf("max/%s/mode/set", srcAddr),
		"modes":                        []string{"auto", "heat", "off"},
		"preset_mode_command_topic":    fmt.Sprintf("max/%s/preset/set", srcAddr),
		"preset_mode_state_topic":      fmt.Sprintf(TopicFormatState, srcAddr),
		"preset_mode_value_template":   "{{ value_json.get('preset_mode', 'none') }}",
		"preset_modes":                 []string{"eco", "comfort", "boost"},
		"min_temp":                     5,
		"max_temp":                     30,
		"temp_step":                    0.5,
		"precision":                    0.1,
		"temperature_unit":             "C",
		"action_topic":                 fmt.Sprintf(TopicFormatState, srcAddr),
		"action_template":              "{{ value_json.get('hvac_action') }}",
		"device":                       deviceInfo,
	}
	publishDiscovery(climateTopic, climatePayload)

	battTopic := fmt.Sprintf("homeassistant/binary_sensor/%s_battery/config", deviceID)
	battPayload := map[string]interface{}{
		"name":           "Battery",
		"unique_id":      deviceID + "_battery",
		"device_class":   "battery",
		"state_topic":    fmt.Sprintf(TopicFormatState, srcAddr),
		"value_template": "{{ 'ON' if value_json.get('battery') == 'low' else 'OFF' }}",
		"device":         deviceInfo,
	}
	publishDiscovery(battTopic, battPayload)

	rfTopic := fmt.Sprintf("homeassistant/binary_sensor/%s_rf_error/config", deviceID)
	rfPayload := map[string]interface{}{
		"name":            "RF Error",
		"unique_id":       deviceID + "_rf_error",
		"device_class":    "problem",
		"entity_category": "diagnostic",
		"state_topic":     fmt.Sprintf(TopicFormatState, srcAddr),
		"value_template":  "{{ 'ON' if value_json.get('rf_error') else 'OFF' }}",
		"device":          deviceInfo,
	}
	publishDiscovery(rfTopic, rfPayload)

	if isRadiatorDevice(srcAddr) {
		valveTopic := fmt.Sprintf("homeassistant/sensor/%s_valve_position/config", deviceID)
		valvePayload := map[string]interface{}{
			"name":                "Valve Position",
			"unique_id":           deviceID + "_valve_position",
			"icon":                "mdi:valve",
			"unit_of_measurement": "%",
			"state_class":         "measurement",
			"state_topic":         fmt.Sprintf(TopicFormatValve, srcAddr),
			"device":              deviceInfo,
		}
		publishDiscovery(valveTopic, valvePayload)
	}

	untilTopic := fmt.Sprintf("homeassistant/sensor/%s_vacation_until/config", deviceID)
	untilPayload := map[string]interface{}{
		"name":           "Vacation Until",
		"unique_id":      deviceID + "_vacation_until",
		"icon":           "mdi:airplane",
		"state_topic":    fmt.Sprintf(TopicFormatState, srcAddr),
		"value_template": "{{ value_json.get('until') or 'None' }}",
		"device":         deviceInfo,
	}
	publishDiscovery(untilTopic, untilPayload)

	publishConfigEntities(srcAddr)
	publishWeekProfileDiscovery(srcAddr)
	publishDeviceConfig(srcAddr)
}

// publishValvePosition publishes the valve position (0-100) retained on max/<id>/valve
func publishValvePosition(srcAddr string, valve int) {
	topic := fmt.Sprintf(TopicFormatValve, srcAddr)
	slog.Debug("MQTT PUB valve position", "topic", topic, "valve", valve)
	token := mqttClient.Publish(topic, 0, true, strconv.Itoa(valve))
	token.Wait()
}

func publishBridgeDiscovery() {
	topic := "homeassistant/button/max_bridge_pair/config"
	payload := map[string]interface{}{
		"name":            "MAX! Pairing Mode",
		"unique_id":       "max_bridge_pair",
		"icon":            "mdi:plus-network",
		"entity_category": "config",
		"command_topic":   "max/bridge/pair",
		"payload_press":   "on",
		"device":          bridgeDeviceInfo(),
	}
	publishDiscovery(topic, payload)
	publishDutyCycleDiscovery(bridgeDeviceInfo())
}

func bridgeDeviceInfo() map[string]string {
	return map[string]string{
		"identifiers":  "max_bridge",
		"manufacturer": "max2mqtt",
		"model":        "MAX! to MQTT Bridge",
		"name":         "MAX! Bridge",
	}
}

func publishState(srcAddr string, data *MaxDeviceData) {
	deviceID := "max_" + srcAddr
	topic := fmt.Sprintf(TopicFormatState, srcAddr)

	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("Failed to marshal state", "device", deviceID, "error", err)
		return
	}

	// Enhanced Logging to show Entity Mapping
	slog.Debug("MQTT PUB (Broadcast to all entities)", "topic", topic)
	slog.Debug("  -> Climate (Temp)", "temp", data.Temperature)
	if data.CurrentTemperature != nil {
		slog.Debug("  -> Climate (CurTemp)", "cur_temp", *data.CurrentTemperature)
	}
	slog.Debug("  -> Climate (HVAC)", "hvac", data.HVACMode)
	slog.Debug("  -> Internal Mode", "mode", data.Mode)

	slog.Debug("  -> Binary (Battery)", "battery", data.Battery)
	slog.Debug("  Raw Payload", "payload", string(payload))

	token := mqttClient.Publish(topic, 0, true, payload) // Changed to retained=true
	token.Wait()
}

func handleMQTTCommand(client mqtt.Client, msg mqtt.Message) {
	// Topic: max/[ID]/set
	// Payload: Temperature float (e.g. "21.5")
	topicParts := strings.Split(msg.Topic(), "/")
	if len(topicParts) < 3 {
		return
	}
	srcAddr := strings.ToUpper(topicParts[1])
	payloadStr := strings.TrimSpace(string(msg.Payload()))

	slog.Info("Received command", "src", srcAddr, "payload", payloadStr)

	temp, err := strconv.ParseFloat(payloadStr, 64)
	if err != nil {
		slog.Error("Invalid temperature format", "payload", payloadStr)
		return
	}

	slog.Info("Processing set temperature command", "temp", temp, "src", srcAddr)

	// Preserve current mode from state cache (default to Manual if unknown)
	modeBits := getModeFromCache(srcAddr)
	modeStr := "manual"
	hvacMode := "heat"
	// If device is in boost/vacation, applying a setpoint should switch to manual
	if modeBits == 0x02 || modeBits == 0x03 {
		slog.Debug("SetTemp: boost/vacation → Manual for setpoint", "src", srcAddr, "prev_mode_bits", modeBits)
		modeBits = 0x01
	}
	switch modeBits {
	case 0x00:
		modeStr, hvacMode = "auto", "auto"
	case 0x01:
		modeStr, hvacMode = "manual", "heat"
		if temp <= 4.5 {
			hvacMode = "off"
		}
	}

	tempVal := byte(temp * 2)
	payloadByte := tempVal | (modeBits << 6)
	slog.Debug("SetTemp encoding",
		"src", srcAddr,
		"temp", temp,
		"mode", modeStr,
		"hvac", hvacMode,
		"mode_bits", fmt.Sprintf("0x%02X", modeBits),
		"payload_byte", fmt.Sprintf("0x%02X", payloadByte),
		"group", getDeviceGroupID(srcAddr),
		"gateway_id", config.GatewayID,
		"needs_wakeup", deviceNeedsWakeUp(srcAddr),
	)

	maybeWakeDevice(srcAddr)
	if ackTracker != nil {
		ackTracker.TrackSetpoint(srcAddr, temp, modeStr, hvacMode)
	}
	sendCommand(srcAddr, 0x40, []byte{payloadByte}, fmt.Sprintf("Set Temp %.1f", temp))

	if verificationMgr != nil {
		verificationMgr.AddVerification(srcAddr, temp, func() {
			slog.Warn("Retrying Set Temp", "temp", temp, "src", srcAddr)
			maybeWakeDevice(srcAddr)
			sendCommand(srcAddr, 0x40, []byte{payloadByte}, fmt.Sprintf("Retry Set Temp %.1f", temp))
		})
	}
}

// maybeWakeDevice sends WakeUp (0xF1) before commanding sleepy radiator valves.
func maybeWakeDevice(dstAddr string) {
	if !deviceNeedsWakeUp(dstAddr) {
		slog.Debug("WakeUp skipped (device recently heard)", "dst", dstAddr)
		return
	}
	slog.Info("Sending WakeUp before command (device quiet/unknown)", "dst", dstAddr)
	sendCommand(dstAddr, 0xF1, []byte{0x3F}, "WakeUp")
}

func handleMQTTPair(client mqtt.Client, msg mqtt.Message) {
	slog.Info("Activating Pairing Mode for 60 seconds")
	pairingUntil = time.Now().Add(60 * time.Second)
}

func handleMQTTModeCommand(client mqtt.Client, msg mqtt.Message) {
	// Topic: max/[ID]/mode/set
	// Payload: Mode string (auto, manual, boost, vacation)
	topicParts := strings.Split(msg.Topic(), "/")
	if len(topicParts) < 4 {
		return
	}
	srcAddr := strings.ToUpper(topicParts[1])
	mode := strings.TrimSpace(string(msg.Payload()))

	slog.Info("Received mode command", "src", srcAddr, "mode", mode)

	// Determine Mode Bits and Target Temp
	var modeBits byte
	var targetTemp float64 = 21.0 // Default fallback

	// Retrieve current state for temp preservation
	stateMutex.RLock()
	if existing, ok := stateCache[srcAddr]; ok && existing.Temperature > 0 {
		targetTemp = existing.Temperature
		slog.Debug("Preserving current temperature for mode change", "temp", targetTemp, "mode", mode)
	}
	stateMutex.RUnlock()

	switch mode {
	case "auto":
		modeBits = 0x00
		// Auto with temperature 0 = follow the week program (FHEM "desiredTemperature auto",
		// MAX! Cube). A temperature here would be an override until the next switch point.
		targetTemp = 0
	case "heat": // Manual
		modeBits = 0x01
		// Use current setpoint
	case "off":
		modeBits = 0x01 // Manual
		targetTemp = 4.5
	case "boost": // Boost
		modeBits = 0x03
		// Boost is sent with temperature 0 (FHEM); the device keeps its setpoint for the time after the boost
	default:
		slog.Warn("Unknown mode", "mode", mode)
		return
	}

	tempVal := byte(targetTemp * 2)
	if modeBits == 0x03 {
		tempVal = 0
	}
	payloadByte := tempVal | (modeBits << 6)

	modeStr := "manual"
	hvacMode := mode
	switch mode {
	case "auto":
		modeStr, hvacMode = "auto", "auto"
	case "heat":
		modeStr, hvacMode = "manual", "heat"
	case "off":
		modeStr, hvacMode = "manual", "off"
	case "boost":
		modeStr, hvacMode = "boost", "heat"
	}

	maybeWakeDevice(srcAddr)
	if ackTracker != nil {
		ackTracker.TrackSetpoint(srcAddr, targetTemp, modeStr, hvacMode)
	}
	sendCommand(srcAddr, 0x40, []byte{payloadByte}, fmt.Sprintf("Set Mode %s", mode))
}

// nextMsgCounter increments and returns the global message counter (1-255, wrapping)
func nextMsgCounter() int {
	msgCounterMutex.Lock()
	defer msgCounterMutex.Unlock()
	msgCounter++
	if msgCounter > 255 {
		msgCounter = 1
	}
	return msgCounter
}

// getGatewayAddress returns the 3-byte gateway address from config
// Falls back to default if config is invalid
func getGatewayAddress() []byte {
	srcBytes, _ := hex.DecodeString(config.GatewayID)
	if len(srcBytes) != 3 {
		return []byte{0x12, 0x34, 0x56} // Fallback
	}
	return srcBytes
}

// gatewayAddrHex returns the gateway address as 6 uppercase hex digits (as used in parsed packets)
func gatewayAddrHex() string {
	return fmt.Sprintf("%06X", getGatewayAddress())
}

// sendCommand constructs and sends a MAX! message via CUL
func sendCommand(dstAddr string, typeByte byte, payload []byte, description string) {
	queueCommand(dstAddr, typeByte, payload, description, false)
}

// sendBulkCommand sends one message of a long transfer (week profile): it may wait longer
// for duty cycle credits and lets normal commands go first
func sendBulkCommand(dstAddr string, typeByte byte, payload []byte, description string) {
	queueCommand(dstAddr, typeByte, payload, description, true)
}

func queueCommand(dstAddr string, typeByte byte, payload []byte, description string, bulk bool) {
	dstAddr = strings.ToUpper(dstAddr)
	cnt := nextMsgCounter()
	srcBytes := getGatewayAddress()

	dstBytes, _ := hex.DecodeString(dstAddr)
	if len(dstBytes) != 3 {
		slog.Error("Invalid destination address", "addr", dstAddr)
		return
	}

	// Structure: Len(1) | Cnt(1) | Flg(1) | Type(1) | Src(3) | Dst(3) | Grp(1) | Payload(...)
	packetLen := 10 + len(payload)

	// FHEM/Cube: flag 0x04 for broadcast OR when addressing a room group;
	// groupId must match the device's room (learned from RX), else SetTemperature is ignored.
	groupID := byte(0)
	if dstAddr != "000000" {
		groupID = byte(getDeviceGroupID(dstAddr))
	}
	var flag byte = 0x00
	if dstAddr == "000000" || groupID != 0 {
		flag = 0x04
	}

	packet := make([]byte, 0, packetLen+1)
	packet = append(packet, byte(packetLen))
	packet = append(packet, byte(cnt))
	packet = append(packet, flag)
	packet = append(packet, typeByte)
	packet = append(packet, srcBytes...)
	packet = append(packet, dstBytes...)
	packet = append(packet, groupID)
	packet = append(packet, payload...)

	hexStr := hex.EncodeToString(packet)
	cmd := "Zs" + hexStr

	slog.Info("Queueing TX", "dst", dstAddr, "desc", description, "group", groupID, "flag", fmt.Sprintf("0x%02X", flag), "hex", hexStr)
	slog.Debug("TX packet fields",
		"dst", dstAddr,
		"src", config.GatewayID,
		"type", fmt.Sprintf("0x%02X", typeByte),
		"cnt", cnt,
		"group", groupID,
		"flag", fmt.Sprintf("0x%02X", flag),
		"payload", fmt.Sprintf("%X", payload),
		"cul_cmd", cmd,
	)

	if txMgr != nil {
		descWithCnt := fmt.Sprintf("%s (Cnt:%d grp:%d)", description, cnt, groupID)
		if bulk {
			txMgr.EnqueueBulk(dstAddr, cmd, descWithCnt)
		} else {
			txMgr.Enqueue(dstAddr, cmd, descWithCnt)
		}
	} else {
		select {
		case serialWrite <- cmd:
			slog.Info("TX", "dst", dstAddr, "desc", description, "hex", hexStr)
		default:
			slog.Error("Serial write queue full")
		}
	}
}

func sendPairPong(targetAddr string) {
	// PairPong: Type 0x01
	// To send via CUL: "Zs" + HexString
	cnt := nextMsgCounter()
	srcBytes := getGatewayAddress()
	dstBytes, _ := hex.DecodeString(targetAddr)

	// Payload must be 5 bytes to hit Len 0x0E
	// Byte 0: Group (0x00)
	// Byte 1-4: Unknown/Padding? (0x00)
	payload := []byte{0x00, 0x00, 0x00, 0x00, 0x00}

	// Structure: Len | Cnt | Flg | Type | Src | Dst | Payload
	// Fields after Len: Cnt(1) + Flg(1) + Type(1) + Src(3) + Dst(3) + Payload(5) = 14 bytes (0x0E)
	msgLen := 0x0E

	// Pkt: Len, Cnt, Flg(0x00), Type(0x01)
	packet := []byte{byte(msgLen), byte(cnt), 0x00, 0x01}
	packet = append(packet, srcBytes...)
	packet = append(packet, dstBytes...)
	packet = append(packet, payload...)

	hexStr := hex.EncodeToString(packet)
	// Send as CUL send command (Zs = Send raw)
	cmd := "Zs" + hexStr

	select {
	case serialWrite <- cmd:
		slog.Info("Sent PairPong", "target", targetAddr, "cnt", cnt)
		// Schedule time sync for newly paired device (direct, not broadcast)
		slog.Info("Scheduling time sync for newly paired device", "target", targetAddr)
		go func() {
			time.Sleep(2 * time.Second)
			sendTimeToDevice(targetAddr)
		}()
	default:
		slog.Error("Serial write queue full")
	}
}

func handleMQTTEcoTemp(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "eco_temp", 4.5, 30.5, func(c *DeviceConfig, v float64) { c.EcoTemp = v })
}

func handleMQTTComfortTemp(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "comfort_temp", 4.5, 30.5, func(c *DeviceConfig, v float64) { c.ComfortTemp = v })
}

func handleMQTTMaxTemp(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "max_temp", 4.5, 30.5, func(c *DeviceConfig, v float64) { c.MaxTemp = v })
}

func handleMQTTMinTemp(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "min_temp", 4.5, 30.5, func(c *DeviceConfig, v float64) { c.MinTemp = v })
}

func handleMQTTMeasurementOffset(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "measurement_offset", -3.5, 3.5, func(c *DeviceConfig, v float64) { c.MeasurementOffset = v })
}

func handleMQTTWindowOpenTemp(client mqtt.Client, msg mqtt.Message) {
	handleMQTTFloatConfig(msg, "window_open_temp", 4.5, 30.5, func(c *DeviceConfig, v float64) { c.WindowOpenTemp = v })
}

func handleMQTTWindowOpenDuration(client mqtt.Client, msg mqtt.Message) {
	handleMQTTIntConfig(msg, "window_open_duration", 0, 60, 5, func(c *DeviceConfig, v int) { c.WindowOpenDuration = v })
}

func handleMQTTDisplayMode(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	payload := string(msg.Payload())
	showActual := strings.EqualFold(payload, "Actual")
	mode := "Setpoint"
	if showActual {
		mode = "Actual"
	}
	prev := snapshotDevice(deviceID)
	next := prev
	next.Display = mode
	ackTracker.TrackConfig(deviceID, 0x82, "display_mode", prev, next)
	sendDisplayMode(deviceID, showActual)
}

func handleMQTTGroupID(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil || val < 0 || val > 255 {
		slog.Error("Invalid group_id", "payload", string(msg.Payload()))
		return
	}
	prev := snapshotDevice(deviceID)
	next := prev
	next.GroupID = val
	ackTracker.TrackConfig(deviceID, 0x22, "group_id", prev, next)
	sendSetGroupID(deviceID, val)
}

func handleMQTTBoostDuration(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil {
		return
	}
	if _, ok := boostDurationToNibble[val]; !ok {
		slog.Error("Invalid boost_duration (use 0,5,10,15,20,25,30,60)", "value", val)
		return
	}
	beginValveChange(deviceID, "boost_duration", func(v *ValveConfig) { v.BoostDuration = val })
}

func handleMQTTBoostValvePosition(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil || val < 0 || val > 100 {
		return
	}
	val = (val / 5) * 5
	beginValveChange(deviceID, "boost_valve_position", func(v *ValveConfig) { v.BoostValvePosition = val })
}

func handleMQTTDecalcDay(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	day := strings.TrimSpace(string(msg.Payload()))
	if _, ok := decalcDayToNibble[day]; !ok {
		slog.Error("Invalid decalc_day", "value", day)
		return
	}
	beginValveChange(deviceID, "decalc_day", func(v *ValveConfig) { v.DecalcDay = day })
}

func handleMQTTDecalcHour(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil || val < 0 || val > 23 {
		return
	}
	beginValveChange(deviceID, "decalc_hour", func(v *ValveConfig) { v.DecalcHour = val })
}

func handleMQTTMaxValveSetting(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil || val < 0 || val > 100 {
		return
	}
	beginValveChange(deviceID, "max_valve_setting", func(v *ValveConfig) { v.MaxValveSetting = val })
}

func handleMQTTValveOffset(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil || val < 0 || val > 100 {
		return
	}
	beginValveChange(deviceID, "valve_offset", func(v *ValveConfig) { v.ValveOffset = val })
}

func handleMQTTFactoryReset(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	slog.Warn("Factory reset requested", "device", deviceID)
	sendFactoryReset(deviceID)
}

func handleMQTTDisassociate(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	payload := strings.TrimSpace(string(msg.Payload()))
	partner := payload
	var partnerType byte = 1
	if strings.Contains(payload, ":") {
		parts := strings.SplitN(payload, ":", 2)
		partner = parts[0]
		if t, err := strconv.Atoi(parts[1]); err == nil {
			partnerType = byte(t)
		}
	}
	if len(partner) != 6 {
		slog.Error("disassociate payload must be PARTNER or PARTNER:type", "payload", payload)
		return
	}
	sendRemoveLinkPartner(deviceID, strings.ToUpper(partner), partnerType)
}

func handleMQTTPresetCommand(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	deviceID = strings.ToUpper(deviceID)
	preset := strings.ToLower(strings.TrimSpace(string(msg.Payload())))
	cfg := getDeviceConfig(deviceID)
	var modeBits byte
	var targetTemp float64

	configMutex.RLock()
	eco, comfort, tempsKnown := cfg.EcoTemp, cfg.ComfortTemp, cfg.TempsKnown
	configMutex.RUnlock()
	if (preset == "eco" || preset == "comfort") && !tempsKnown {
		slog.Error("Eco/comfort temperature of this device is unknown; import it first (max/<id>/config/import)", "device", deviceID, "preset", preset)
		return
	}

	switch preset {
	case "eco":
		modeBits = 0x01
		targetTemp = eco
	case "comfort":
		modeBits = 0x01
		targetTemp = comfort
	case "boost":
		// Sent with temperature 0 (FHEM). The cached setpoint stays, so a later
		// heat/none/auto command does not turn the boost value into a setpoint
		modeBits = 0x03
	case "none", "":
		// Clear preset: keep current mode/temp via heat/manual
		modeBits = 0x01
		stateMutex.RLock()
		if existing, ok := stateCache[deviceID]; ok && existing.Temperature > 0 {
			targetTemp = existing.Temperature
		} else {
			targetTemp = comfort
		}
		stateMutex.RUnlock()
		preset = "none"
	default:
		slog.Warn("Unknown preset", "preset", preset)
		return
	}

	tempVal := byte(targetTemp * 2)
	payloadByte := tempVal | (modeBits << 6)
	sendCommand(deviceID, 0x40, []byte{payloadByte}, fmt.Sprintf("Set Preset %s", preset))

	stateMutex.Lock()
	if existing, ok := stateCache[deviceID]; ok {
		existing.PresetMode = preset
		if preset != "boost" {
			existing.Temperature = targetTemp
		}
		if preset == "boost" {
			existing.Mode = "boost"
			existing.HVACMode = "heat"
		} else if preset != "none" {
			existing.Mode = "manual"
			existing.HVACMode = "heat"
		}
		pub := *existing
		stateMutex.Unlock()
		publishState(deviceID, &pub)
	} else {
		stateMutex.Unlock()
	}

	if verificationMgr != nil && preset != "boost" {
		verificationMgr.AddVerification(deviceID, targetTemp, func() {
			sendCommand(deviceID, 0x40, []byte{payloadByte}, fmt.Sprintf("Retry Preset %s", preset))
		})
	}
}

// sendTimeBroadcast sends Type 0x03 TimeInformation to broadcast address 000000
// All devices paired with this gateway will receive and accept this message.
func sendTimeBroadcast() {
	payload := buildTimePayload()
	now := time.Now()
	slog.Info("Broadcasting time sync", "time", fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d",
		now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second()))
	sendCommand("000000", 0x03, payload, "Time Broadcast")
}

// sendTimeToDevice sends Type 0x03 TimeInformation to a specific device address
// This is used to respond to time requests from devices or to sync time after pairing
func sendTimeToDevice(dstAddr string) {
	payload := buildTimePayload()
	now := time.Now()

	slog.Info("Sending time to device", "src", dstAddr, "time", fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d",
		now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second()))
	sendCommand(dstAddr, 0x03, payload, fmt.Sprintf("Time to %s", dstAddr))
}

// buildTimePayload constructs the 5-byte time payload for MAX! protocol
// Payload format:
//   - Byte 0: Year (since 2000)
//   - Byte 1: Day of month
//   - Byte 2: Hour
//   - Byte 3: Minute | ((Month & 0x0C) << 4)
//   - Byte 4: Second | ((Month & 0x03) << 6)
func buildTimePayload() []byte {
	now := time.Now()
	year := byte(now.Year() - 2000)
	month := byte(now.Month())
	day := byte(now.Day())
	hour := byte(now.Hour())
	min := byte(now.Minute())
	sec := byte(now.Second())

	compressedOne := min | ((month & 0x0C) << 4)
	compressedTwo := sec | ((month & 0x03) << 6)

	return []byte{year, day, hour, compressedOne, compressedTwo}
}

// getModeFromCache retrieves the current mode bits for a device from state cache.
// Returns 0x01 (Manual) as default if device state is not cached.
func getModeFromCache(srcAddr string) byte {
	stateMutex.RLock()
	defer stateMutex.RUnlock()

	existing, ok := stateCache[srcAddr]
	if !ok {
		slog.Warn("No cached state, defaulting to Manual mode", "src", srcAddr)
		return 0x01
	}

	modeBits := modeStringToBits(existing.Mode)
	return modeBits

}

// modeStringToBits converts a mode string to MAX! protocol mode bits
// Mode bits: 00=Auto, 01=Manual, 10=Vacation, 11=Boost
func modeStringToBits(mode string) byte {
	switch mode {
	case "auto":
		return 0x00
	case "manual":
		return 0x01
	case "vacation":
		return 0x02
	case "boost":
		return 0x03
	default:
		return 0x01 // Default to Manual
	}
}

// startPeriodicTimeSync starts a goroutine that broadcasts time on startup
// and then every 1 hour to keep all paired devices synchronized.
func startPeriodicTimeSync() {
	// Initial delay to allow serial connection to be established
	time.Sleep(10 * time.Second)

	for {
		slog.Info("Sending periodic time broadcast")
		sendTimeBroadcast()
		time.Sleep(1 * time.Hour)
	}
}

// handleMQTTAssociate links two MAX! devices together (device-to-device association)
// Topic: max/<source_device>/associate
// Payload: <partner_device_id> or <partner_device_id>:<partner_type>
// Partner types: 1=HeatingThermostat, 2=HeatingThermostatPlus, 3=WallMountedThermostat, 4=ShutterContact, 5=PushButton
// If partner_type is omitted, defaults to 1 (radiator thermostat)
//
// Example: To link radiator 0A1B2C with wall thermostat 0D3E4F:
//
//	mosquitto_pub -t "max/0A1B2C/associate" -m "0D3E4F:3"  # Tell radiator about wall (type 3)
//	mosquitto_pub -t "max/0D3E4F/associate" -m "0A1B2C:1"  # Tell wall about radiator (type 1)
//
// Note: Association must be done in BOTH directions for devices to work together.
func handleMQTTAssociate(client mqtt.Client, msg mqtt.Message) {
	topicParts := strings.Split(msg.Topic(), "/")
	if len(topicParts) < 3 {
		return
	}
	srcDevice := topicParts[1]
	payload := string(msg.Payload())

	// Validate source device address (6 hex chars)
	if len(srcDevice) != 6 {
		slog.Error("Invalid source device address (must be 6 hex chars)", "src", srcDevice)
		return
	}

	// Parse payload: "<partner_id>" or "<partner_id>:<type>"
	var partnerDevice string
	var partnerType byte = 1 // Default to HeatingThermostat

	if strings.Contains(payload, ":") {
		parts := strings.Split(payload, ":")
		if len(parts) != 2 {
			slog.Error("Invalid associate payload format (expected 'device_id' or 'device_id:type')", "payload", payload)
			return
		}
		partnerDevice = strings.TrimSpace(parts[0])
		typeVal, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || typeVal < 1 || typeVal > 5 {
			slog.Error("Invalid partner type (must be 1-5)", "type", parts[1])
			return
		}
		partnerType = byte(typeVal)
	} else {
		partnerDevice = strings.TrimSpace(payload)
	}

	// Validate partner device address
	if len(partnerDevice) != 6 {
		slog.Error("Invalid partner device address (must be 6 hex chars)", "partner", partnerDevice)
		return
	}

	// Convert to uppercase for consistency
	srcDevice = strings.ToUpper(srcDevice)
	partnerDevice = strings.ToUpper(partnerDevice)

	sendAddLinkPartner(srcDevice, partnerDevice, partnerType)
}

// sendAddLinkPartner sends Type 0x20 AddLinkPartner command
// Links srcDevice to partnerDevice so they communicate with each other
// partnerType values:
//   - 1: Heating Thermostat (radiator valve)
//   - 2: Heating Thermostat Plus
//   - 3: Wall Mounted Thermostat
//   - 4: Shutter Contact
//   - 5: Push Button
//
// Payload structure (4 bytes) per FHEM reference:
//   - Bytes 0-2: Partner RF Address (3 bytes)
//   - Byte 3: Partner Type
func sendAddLinkPartner(srcDevice, partnerDevice string, partnerType byte) {
	partnerBytes, err := hex.DecodeString(partnerDevice)
	if err != nil || len(partnerBytes) != 3 {
		slog.Error("Invalid partner device address", "partner", partnerDevice)
		return
	}

	// Payload: PartnerAddr(3) + PartnerType(1) = 4 bytes
	payload := make([]byte, 4)
	payload[0] = partnerBytes[0]
	payload[1] = partnerBytes[1]
	payload[2] = partnerBytes[2]
	payload[3] = partnerType

	typeNames := map[byte]string{
		1: "HeatingThermostat",
		2: "HeatingThermostatPlus",
		3: "WallMountedThermostat",
		4: "ShutterContact",
		5: "PushButton",
	}
	typeName := typeNames[partnerType]
	if typeName == "" {
		typeName = fmt.Sprintf("Unknown(%d)", partnerType)
	}

	description := fmt.Sprintf("AddLinkPartner %s -> %s (%s)", srcDevice, partnerDevice, typeName)
	sendCommand(srcDevice, 0x20, payload, description)
}

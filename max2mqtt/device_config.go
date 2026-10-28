package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// DeviceConfig stores ConfigTemperatures (0x11) plus display/group metadata.
type DeviceConfig struct {
	ComfortTemp        float64 `json:"comfort_temp"`
	EcoTemp            float64 `json:"eco_temp"`
	MaxTemp            float64 `json:"max_temp"`
	MinTemp            float64 `json:"min_temp"`
	MeasurementOffset  float64 `json:"measurement_offset"`
	WindowOpenTemp     float64 `json:"window_open_temp"`
	WindowOpenDuration int     `json:"window_open_duration"`
	DisplayMode        string  `json:"display_mode"` // Setpoint | Actual
	GroupID            int     `json:"group_id"`
	IsWall             bool    `json:"is_wall"`
	IsRadiator         bool    `json:"is_radiator"`
	PresetMode         string  `json:"preset_mode"` // none | eco | comfort | boost
	// TempsKnown: the ConfigTemperatures values above are the device's real values
	// (imported, or written by the bridge). MAX! devices cannot be asked for them.
	TempsKnown bool `json:"temps_known"`
}

// ValveConfig stores ConfigValve (0x12) parameters for radiator thermostats.
type ValveConfig struct {
	BoostDuration      int    `json:"boost_duration"`       // minutes: 0,5,10,15,20,25,30,60
	BoostValvePosition int    `json:"boost_valve_position"` // 0-100
	DecalcDay          string `json:"decalc_day"`           // Sat..Fri
	DecalcHour         int    `json:"decalc_hour"`          // 0-23
	MaxValveSetting    int    `json:"max_valve_setting"`    // 0-100
	ValveOffset        int    `json:"valve_offset"`         // 0-100
	// ValveKnown: the ConfigValve values above are the device's real values (see TempsKnown)
	ValveKnown bool `json:"valve_known"`
}

var (
	valveConfigCache = make(map[string]*ValveConfig)
	valveMutex       sync.RWMutex
)

var boostDurationToNibble = map[int]byte{
	0: 0, 5: 1, 10: 2, 15: 3, 20: 4, 25: 5, 30: 6, 60: 7,
}

var decalcDayToNibble = map[string]byte{
	"Sat": 0, "Sun": 1, "Mon": 2, "Tue": 3, "Wed": 4, "Thu": 5, "Fri": 6,
}

var decalcNibbleToDay = []string{"Sat", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri"}

func getDeviceConfig(deviceAddr string) *DeviceConfig {
	configMutex.RLock()
	cfg, exists := deviceConfigCache[deviceAddr]
	configMutex.RUnlock()
	if exists {
		return cfg
	}

	cfg = &DeviceConfig{
		ComfortTemp:        21.0,
		EcoTemp:            17.0,
		MaxTemp:            30.5,
		MinTemp:            4.5,
		MeasurementOffset:  0.0,
		WindowOpenTemp:     12.0,
		WindowOpenDuration: 15,
		DisplayMode:        "Setpoint",
		GroupID:            0,
		PresetMode:         "none",
	}
	configMutex.Lock()
	deviceConfigCache[deviceAddr] = cfg
	configMutex.Unlock()
	return cfg
}

func getValveConfig(deviceAddr string) *ValveConfig {
	valveMutex.RLock()
	cfg, exists := valveConfigCache[deviceAddr]
	valveMutex.RUnlock()
	if exists {
		return cfg
	}
	cfg = &ValveConfig{
		BoostDuration:      5,
		BoostValvePosition: 80,
		DecalcDay:          "Sat",
		DecalcHour:         12,
		MaxValveSetting:    100,
		ValveOffset:        0,
	}
	valveMutex.Lock()
	valveConfigCache[deviceAddr] = cfg
	valveMutex.Unlock()
	return cfg
}

func markDeviceType(deviceAddr string, pktType int) {
	cfg := getDeviceConfig(deviceAddr)
	configMutex.Lock()
	defer configMutex.Unlock()
	switch pktType {
	case 0x70, 0x42:
		cfg.IsWall = true
	case 0x60:
		cfg.IsRadiator = true
	}
}

func updateDeviceGroupID(deviceAddr string, group int) {
	if group <= 0 {
		return
	}
	cfg := getDeviceConfig(deviceAddr)
	configMutex.Lock()
	changed := cfg.GroupID != group
	cfg.GroupID = group
	configMutex.Unlock()
	if changed {
		slog.Info("Learned device group ID from RX", "device", deviceAddr, "group", group)
		publishDeviceConfig(deviceAddr)
	}
}

func getDeviceGroupID(deviceAddr string) int {
	cfg := getDeviceConfig(deviceAddr)
	configMutex.RLock()
	defer configMutex.RUnlock()
	return cfg.GroupID
}

// DeviceConfigMQTT is the retained payload for max/<id>/config
type DeviceConfigMQTT struct {
	DeviceConfig
	ValveConfig
}

func publishDeviceConfig(deviceAddr string) {
	if mqttClient == nil {
		return
	}
	cfg := getDeviceConfig(deviceAddr)
	valve := getValveConfig(deviceAddr)
	configMutex.RLock()
	temps := *cfg
	configMutex.RUnlock()
	valveMutex.RLock()
	v := *valve
	valveMutex.RUnlock()

	payload := DeviceConfigMQTT{DeviceConfig: temps, ValveConfig: v}
	bytes, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal device config", "error", err)
		return
	}
	topic := fmt.Sprintf("max/%s/config", deviceAddr)
	token := mqttClient.Publish(topic, 0, true, bytes)
	token.Wait()
	slog.Debug("Published device config", "device", deviceAddr)
}

func publishNumberDiscovery(srcAddr, suffix, name, icon, unit string, min, max, step float64, valueTemplate string) {
	deviceID := "max_" + srcAddr
	deviceInfo := discoveryDeviceInfo(srcAddr)
	topic := fmt.Sprintf("homeassistant/number/%s_%s/config", deviceID, suffix)
	payload := map[string]interface{}{
		"name":                name,
		"unique_id":           deviceID + "_" + suffix,
		"icon":                icon,
		"entity_category":     "config",
		"mode":                "box",
		"min":                 min,
		"max":                 max,
		"step":                step,
		"command_topic":       fmt.Sprintf("max/%s/%s/set", srcAddr, suffix),
		"state_topic":         fmt.Sprintf("max/%s/config", srcAddr),
		"value_template":      valueTemplate,
		"device":              deviceInfo,
	}
	if unit != "" {
		payload["unit_of_measurement"] = unit
	}
	bytes, _ := json.Marshal(payload)
	mqttClient.Publish(topic, 0, true, bytes)
}

func publishSelectDiscovery(srcAddr, suffix, name, icon string, options []string, valueTemplate string) {
	deviceID := "max_" + srcAddr
	topic := fmt.Sprintf("homeassistant/select/%s_%s/config", deviceID, suffix)
	payload := map[string]interface{}{
		"name":            name,
		"unique_id":       deviceID + "_" + suffix,
		"icon":            icon,
		"entity_category": "config",
		"command_topic":   fmt.Sprintf("max/%s/%s/set", srcAddr, suffix),
		"state_topic":     fmt.Sprintf("max/%s/config", srcAddr),
		"value_template":  valueTemplate,
		"options":         options,
		"device":          discoveryDeviceInfo(srcAddr),
	}
	bytes, _ := json.Marshal(payload)
	mqttClient.Publish(topic, 0, true, bytes)
}

func publishButtonDiscovery(srcAddr, suffix, name, icon, commandTopic string) {
	deviceID := "max_" + srcAddr
	topic := fmt.Sprintf("homeassistant/button/%s_%s/config", deviceID, suffix)
	payload := map[string]interface{}{
		"name":            name,
		"unique_id":       deviceID + "_" + suffix,
		"icon":            icon,
		"entity_category": "config",
		"command_topic":   commandTopic,
		"device":          discoveryDeviceInfo(srcAddr),
	}
	bytes, _ := json.Marshal(payload)
	mqttClient.Publish(topic, 0, true, bytes)
}

func discoveryDeviceInfo(srcAddr string) map[string]string {
	cfg := getDeviceConfig(srcAddr)
	model := "MAX! Thermostat"
	configMutex.RLock()
	if cfg.IsWall {
		model = "MAX! Wall Thermostat"
	} else if cfg.IsRadiator {
		model = "MAX! Radiator Thermostat"
	}
	configMutex.RUnlock()
	return map[string]string{
		"identifiers":  "max_" + srcAddr,
		"manufacturer": "eQ-3",
		"model":        model,
		"name":         "MAX! " + srcAddr,
	}
}

func publishConfigEntities(srcAddr string) {
	publishNumberDiscovery(srcAddr, "eco_temp", "Eco Temperature", "mdi:leaf", "°C", 4.5, 30.5, 0.5, knownTemplate("eco_temp", "temps_known"))
	publishNumberDiscovery(srcAddr, "comfort_temp", "Comfort Temperature", "mdi:sofa", "°C", 4.5, 30.5, 0.5, knownTemplate("comfort_temp", "temps_known"))
	publishNumberDiscovery(srcAddr, "max_temp", "Maximum Temperature", "mdi:thermometer-high", "°C", 4.5, 30.5, 0.5, knownTemplate("max_temp", "temps_known"))
	publishNumberDiscovery(srcAddr, "min_temp", "Minimum Temperature", "mdi:thermometer-low", "°C", 4.5, 30.5, 0.5, knownTemplate("min_temp", "temps_known"))
	publishNumberDiscovery(srcAddr, "measurement_offset", "Measurement Offset", "mdi:plus-minus", "°C", -3.5, 3.5, 0.1, knownTemplate("measurement_offset", "temps_known"))
	publishNumberDiscovery(srcAddr, "window_open_temp", "Window Open Temperature", "mdi:window-open", "°C", 4.5, 30.5, 0.5, knownTemplate("window_open_temp", "temps_known"))
	publishNumberDiscovery(srcAddr, "window_open_duration", "Window Open Duration", "mdi:timer", "min", 0, 60, 5, knownTemplate("window_open_duration", "temps_known"))
	publishSelectDiscovery(srcAddr, "display_mode", "Display Mode", "mdi:thermometer-lines",
		[]string{"Setpoint", "Actual"}, "{{ value_json.display_mode }}")
	publishNumberDiscovery(srcAddr, "group_id", "Group ID", "mdi:group", "", 0, 255, 1, "{{ value_json.group_id }}")
	publishButtonDiscovery(srcAddr, "factory_reset", "Factory Reset", "mdi:factory",
		fmt.Sprintf("max/%s/factory_reset/set", srcAddr))
	// Disassociate needs partner address: publish PARTNER or PARTNER:type
	disTopic := fmt.Sprintf("homeassistant/text/%s_disassociate/config", "max_"+srcAddr)
	disPayload := map[string]interface{}{
		"name":            "Disassociate Partner",
		"unique_id":       "max_" + srcAddr + "_disassociate",
		"icon":            "mdi:link-off",
		"entity_category": "config",
		"command_topic":   fmt.Sprintf("max/%s/disassociate/set", srcAddr),
		"mode":            "text",
		"device":          discoveryDeviceInfo(srcAddr),
	}
	disBytes, _ := json.Marshal(disPayload)
	mqttClient.Publish(disTopic, 0, true, disBytes)

	cfg := getDeviceConfig(srcAddr)
	configMutex.RLock()
	isRadiator := cfg.IsRadiator || !cfg.IsWall
	configMutex.RUnlock()
	if isRadiator {
		publishNumberDiscovery(srcAddr, "boost_duration", "Boost Duration", "mdi:rocket-launch", "min", 0, 60, 5, knownTemplate("boost_duration", "valve_known"))
		publishNumberDiscovery(srcAddr, "boost_valve_position", "Boost Valve Position", "mdi:valve", "%", 0, 100, 5, knownTemplate("boost_valve_position", "valve_known"))
		publishSelectDiscovery(srcAddr, "decalc_day", "Decalcification Day", "mdi:calendar",
			[]string{"Sat", "Sun", "Mon", "Tue", "Wed", "Thu", "Fri"}, knownTemplate("decalc_day", "valve_known"))
		publishNumberDiscovery(srcAddr, "decalc_hour", "Decalcification Hour", "mdi:clock", "h", 0, 23, 1, knownTemplate("decalc_hour", "valve_known"))
		publishNumberDiscovery(srcAddr, "max_valve_setting", "Max Valve Setting", "mdi:valve", "%", 0, 100, 1, knownTemplate("max_valve_setting", "valve_known"))
		publishNumberDiscovery(srcAddr, "valve_offset", "Valve Offset", "mdi:valve", "%", 0, 100, 1, knownTemplate("valve_offset", "valve_known"))
	}
}

func sendConfigTemperatures(dstAddr string, cfg *DeviceConfig) {
	payload := make([]byte, 7)
	payload[0] = byte(cfg.ComfortTemp * 2)
	payload[1] = byte(cfg.EcoTemp * 2)
	payload[2] = byte(cfg.MaxTemp * 2)
	payload[3] = byte(cfg.MinTemp * 2)
	payload[4] = byte((cfg.MeasurementOffset + 3.5) * 2)
	payload[5] = byte(cfg.WindowOpenTemp * 2)
	payload[6] = byte(cfg.WindowOpenDuration / 5)

	description := fmt.Sprintf("Config Temps (Comfort: %.1f, Eco: %.1f)", cfg.ComfortTemp, cfg.EcoTemp)
	sendCommand(dstAddr, 0x11, payload, description)
}

func sendConfigValve(dstAddr string, v *ValveConfig) {
	boostNibble, ok := boostDurationToNibble[v.BoostDuration]
	if !ok {
		boostNibble = 1 // 5 min
	}
	dayNibble, ok := decalcDayToNibble[v.DecalcDay]
	if !ok {
		dayNibble = 0
	}
	boost := (boostNibble << 5) | byte(v.BoostValvePosition/5)
	decalc := (dayNibble << 5) | byte(v.DecalcHour&0x1F)
	maxValve := byte(v.MaxValveSetting * 255 / 100)
	offset := byte(v.ValveOffset * 255 / 100)
	payload := []byte{boost, decalc, maxValve, offset}
	sendCommand(dstAddr, 0x12, payload, fmt.Sprintf("Config Valve (boost %dm %d%%)", v.BoostDuration, v.BoostValvePosition))
}

func sendDisplayMode(dstAddr string, showActual bool) {
	var payload byte = 0x00
	if showActual {
		payload = 0x04
	}
	displayStr := "setpoint"
	if showActual {
		displayStr = "actual"
	}
	sendCommand(dstAddr, 0x82, []byte{payload}, fmt.Sprintf("Set Display Mode: %s", displayStr))
}

func sendSetGroupID(dstAddr string, groupID int) {
	if groupID <= 0 {
		sendCommand(dstAddr, 0x23, []byte{0x00}, "Remove Group ID")
		return
	}
	sendCommand(dstAddr, 0x22, []byte{byte(groupID)}, fmt.Sprintf("Set Group ID %d", groupID))
}

func sendFactoryReset(dstAddr string) {
	sendCommand(dstAddr, 0xF0, []byte{}, "Factory Reset")
}

func sendRemoveLinkPartner(srcDevice, partnerDevice string, partnerType byte) {
	partnerBytes, err := hexDecodeAddr(partnerDevice)
	if err != nil {
		slog.Error("Invalid partner address", "partner", partnerDevice)
		return
	}
	payload := append(partnerBytes, partnerType)
	sendCommand(srcDevice, 0x21, payload, fmt.Sprintf("Remove Link %s type %d", partnerDevice, partnerType))
}

func hexDecodeAddr(addr string) ([]byte, error) {
	if len(addr) != 6 {
		return nil, fmt.Errorf("need 6 hex chars")
	}
	b := make([]byte, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(addr[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		b[i] = byte(v)
	}
	return b, nil
}

// knownTemplate shows a config value only when it is known; otherwise Home Assistant shows "unknown"
func knownTemplate(field, knownFlag string) string {
	return fmt.Sprintf("{{ value_json.%s if value_json.%s else None }}", field, knownFlag)
}

func beginConfigTempsChange(deviceID, field string, mutate func(*DeviceConfig)) {
	prev := snapshotDevice(deviceID)
	if !prev.Temps.TempsKnown {
		// ConfigTemperatures always writes all 7 values: with unknown values this would
		// overwrite the device's other settings with defaults
		slog.Error("Config temperatures of this device are unknown; not sending a single value. Import them first (max/<id>/config/import) or set all of them (max/<id>/config/set)", "device", deviceID, "field", field)
		publishDeviceConfig(deviceID)
		return
	}
	next := prev
	mutate(&next.Temps)
	ackTracker.TrackConfig(deviceID, 0x11, field, prev, next)
	sendConfigTemperatures(deviceID, &next.Temps)
}

func beginValveChange(deviceID, field string, mutate func(*ValveConfig)) {
	prev := snapshotDevice(deviceID)
	if !prev.Valve.ValveKnown {
		slog.Error("Valve config of this device is unknown; not sending a single value. Import it first (max/<id>/config/import) or set all of it (max/<id>/config/set)", "device", deviceID, "field", field)
		publishDeviceConfig(deviceID)
		return
	}
	next := prev
	mutate(&next.Valve)
	ackTracker.TrackConfig(deviceID, 0x12, field, prev, next)
	sendConfigValve(deviceID, &next.Valve)
}

func topicDeviceID(topic string) (string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 2 {
		return "", false
	}
	return parts[1], true
}

func handleMQTTFloatConfig(msg mqtt.Message, field string, min, max float64, apply func(*DeviceConfig, float64)) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.ParseFloat(string(msg.Payload()), 64)
	if err != nil {
		slog.Error("Invalid float config", "field", field, "payload", string(msg.Payload()))
		return
	}
	if val < min || val > max {
		slog.Error("Config out of range", "field", field, "value", val)
		return
	}
	slog.Info("Setting config", "device", deviceID, "field", field, "value", val)
	beginConfigTempsChange(deviceID, field, func(c *DeviceConfig) { apply(c, val) })
}

func handleMQTTIntConfig(msg mqtt.Message, field string, min, max, step int, apply func(*DeviceConfig, int)) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
	if err != nil {
		f, ferr := strconv.ParseFloat(strings.TrimSpace(string(msg.Payload())), 64)
		if ferr != nil {
			slog.Error("Invalid int config", "field", field, "payload", string(msg.Payload()))
			return
		}
		val = int(f)
	}
	if val < min || val > max {
		slog.Error("Config out of range", "field", field, "value", val)
		return
	}
	if step > 1 {
		val = (val / step) * step
	}
	slog.Info("Setting config", "device", deviceID, "field", field, "value", val)
	beginConfigTempsChange(deviceID, field, func(c *DeviceConfig) { apply(c, val) })
}

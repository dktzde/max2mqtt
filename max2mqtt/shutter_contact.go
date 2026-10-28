package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ShutterContactData is the state of a MAX! window/door contact (ShutterContactState, 0x30)
type ShutterContactData struct {
	Window  string `json:"window"`  // open | closed
	Battery string `json:"battery"` // ok | low
	RFError bool   `json:"rf_error"`
}

// A contact sends the same message (same counter) to the gateway and to each linked
// thermostat; repeats within this window are processed only once.
const shutterRepeatWindow = 10 * time.Second

var (
	shutterMu         sync.Mutex
	shutterLastCnt    = map[string]int{}
	shutterLastTime   = map[string]time.Time{}
	shutterDiscovered = map[string]bool{}
)

// decodeShutterContact decodes the 1-byte ShutterContactState payload (FHEM 10_MAX.pm):
// bits 0-1 != 0 -> open, bit 6 rferror, bit 7 battery low
func decodeShutterContact(payload []byte) *ShutterContactData {
	if len(payload) < 1 {
		return nil
	}
	bits := payload[0]
	data := &ShutterContactData{Window: "closed", Battery: "ok"}
	if bits&0x03 != 0 {
		data.Window = "open"
	}
	if bits&0x40 != 0 {
		data.RFError = true
	}
	if bits&0x80 != 0 {
		data.Battery = "low"
	}
	return data
}

// isShutterRepeat reports whether the packet repeats one already processed
func isShutterRepeat(src string, cnt int, now time.Time) bool {
	shutterMu.Lock()
	defer shutterMu.Unlock()
	last, ok := shutterLastCnt[src]
	repeat := ok && last == cnt && now.Sub(shutterLastTime[src]) < shutterRepeatWindow
	shutterLastCnt[src] = cnt
	shutterLastTime[src] = now
	return repeat
}

func handleShutterContact(pkt *MaxPacket) {
	if isShutterRepeat(pkt.SrcAddr, pkt.MsgCount, time.Now()) {
		slog.Debug("Ignored repeated ShutterContactState", "src", pkt.SrcAddr, "dst", pkt.DstAddr, "cnt", pkt.MsgCount)
		return
	}
	data := decodeShutterContact(pkt.Payload)
	if data == nil {
		slog.Debug("ShutterContactState without payload", "src", pkt.SrcAddr)
		return
	}
	markDeviceType(pkt.SrcAddr, pkt.Type)
	slog.Info("Decoded Shutter Contact", "src", pkt.SrcAddr, "window", data.Window, "battery", data.Battery, "rf_error", data.RFError)

	sendShutterDiscovery(pkt.SrcAddr)

	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("Failed to marshal shutter state", "device", pkt.SrcAddr, "error", err)
		return
	}
	token := mqttClient.Publish(fmt.Sprintf(TopicFormatState, pkt.SrcAddr), 0, true, payload)
	token.Wait()
}

// sendShutterDiscovery publishes window, battery and RF error binary sensors (once per run)
func sendShutterDiscovery(srcAddr string) {
	shutterMu.Lock()
	done := shutterDiscovered[srcAddr]
	shutterDiscovered[srcAddr] = true
	shutterMu.Unlock()
	if done {
		return
	}

	deviceID := "max_" + srcAddr
	deviceInfo := discoveryDeviceInfo(srcAddr)
	stateTopic := fmt.Sprintf(TopicFormatState, srcAddr)

	sensors := []struct {
		suffix string
		cfg    map[string]interface{}
	}{
		{"window", map[string]interface{}{
			"name":           "Window",
			"device_class":   "window",
			"value_template": "{{ 'ON' if value_json.get('window') == 'open' else 'OFF' }}",
		}},
		{"battery", map[string]interface{}{
			"name":           "Battery",
			"device_class":   "battery",
			"value_template": "{{ 'ON' if value_json.get('battery') == 'low' else 'OFF' }}",
		}},
		{"rf_error", map[string]interface{}{
			"name":            "RF Error",
			"device_class":    "problem",
			"entity_category": "diagnostic",
			"value_template":  "{{ 'ON' if value_json.get('rf_error') else 'OFF' }}",
		}},
	}
	for _, s := range sensors {
		s.cfg["unique_id"] = deviceID + "_" + s.suffix
		s.cfg["state_topic"] = stateTopic
		s.cfg["device"] = deviceInfo
		publishDiscovery(fmt.Sprintf("homeassistant/binary_sensor/%s_%s/config", deviceID, s.suffix), s.cfg)
	}
}

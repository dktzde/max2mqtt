package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Vacation mode (mode bits 10): the thermostat holds a temperature until a date/time,
// then returns to auto by itself. SetTemperature (0x40) gets three extra "until" bytes,
// as in FHEM 10_MAX.pm "desiredTemperature <temp> until <date> <time>".

const untilLayout = "2006-01-02 15:04"

// Accepted formats for "until"; HA datetime helpers use "2006-01-02 15:04:05"
var untilInputLayouts = []string{
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
}

// VacationCommand is the JSON payload of max/<id>/vacation/set
type VacationCommand struct {
	Temperature *float64 `json:"temperature"`
	Until       string   `json:"until"`
}

// encodeUntil packs a date/time into the 3 MAX! "until" bytes:
// Byte 0: month bits 1-3 (bits 5-7) | day; Byte 1: month bit 0 (bit 7) | year-2000; Byte 2: half hours since midnight
func encodeUntil(t time.Time) []byte {
	month := byte(t.Month())
	return []byte{
		(month&0x0E)<<4 | byte(t.Day()),
		(month&0x01)<<7 | byte(t.Year()-2000),
		byte(t.Hour()*2 + t.Minute()/30),
	}
}

// decodeUntil unpacks the 3 MAX! "until" bytes to "YYYY-MM-DD HH:MM" (FHEM MAX_ParseDateTime)
func decodeUntil(b []byte) string {
	if len(b) < 3 {
		return ""
	}
	day := b[0] & 0x1F
	month := (b[0]&0xE0)>>4 | b[1]>>7
	year := 2000 + int(b[1]&0x3F)
	slot := int(b[2] & 0x3F)
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d", year, month, day, slot/2, slot%2*30)
}

// parseVacationCommand validates a vacation payload. The temperature defaults to defaultTemp
// (the device's eco temperature); "until" is rounded down to a full or half hour.
func parseVacationCommand(payload string, defaultTemp float64, now time.Time) (float64, time.Time, error) {
	var cmd VacationCommand
	if err := json.Unmarshal([]byte(payload), &cmd); err != nil {
		return 0, time.Time{}, fmt.Errorf("invalid JSON: %w", err)
	}

	temp := defaultTemp
	if cmd.Temperature != nil {
		temp = *cmd.Temperature
	} else if defaultTemp == 0 {
		return 0, time.Time{}, errors.New(`missing "temperature" (the device's eco temperature is unknown, see max/<id>/config/import)`)
	}
	temp = math.Round(temp*2) / 2
	if temp < 4.5 || temp > 30.5 {
		return 0, time.Time{}, fmt.Errorf("temperature %.1f out of range 4.5-30.5", temp)
	}

	untilStr := strings.TrimSpace(cmd.Until)
	if untilStr == "" {
		return 0, time.Time{}, errors.New(`missing "until"`)
	}
	var until time.Time
	var err error
	for _, layout := range untilInputLayouts {
		until, err = time.ParseInLocation(layout, untilStr, now.Location())
		if err == nil {
			break
		}
	}
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("invalid until %q, use YYYY-MM-DD HH:MM", untilStr)
	}

	// The device only knows full and half hours
	until = time.Date(until.Year(), until.Month(), until.Day(), until.Hour(), until.Minute()/30*30, 0, 0, until.Location())
	if !until.After(now) {
		return 0, time.Time{}, fmt.Errorf("until %s is not in the future", until.Format(untilLayout))
	}
	if until.Year() > 2063 {
		return 0, time.Time{}, fmt.Errorf("until %s is too far ahead", until.Format(untilLayout))
	}
	return temp, until, nil
}

func handleMQTTVacation(client mqtt.Client, msg mqtt.Message) {
	// Topic: max/[ID]/vacation/set
	// Payload: {"temperature": 12, "until": "2026-11-02 18:00"} (temperature optional, default eco temperature)
	//          or "off" to end vacation mode early (back to auto, week program temperature)
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	deviceID = strings.ToUpper(deviceID)
	payloadStr := strings.TrimSpace(string(msg.Payload()))
	slog.Info("Received vacation command", "src", deviceID, "payload", payloadStr)

	if strings.EqualFold(payloadStr, "off") {
		// Auto with temperature 0 = follow the week program (FHEM "desiredTemperature auto")
		maybeWakeDevice(deviceID)
		sendCommand(deviceID, 0x40, []byte{0x00}, "End Vacation")
		return
	}

	cfg := getDeviceConfig(deviceID)
	configMutex.RLock()
	eco, ecoKnown := cfg.EcoTemp, cfg.TempsKnown
	configMutex.RUnlock()
	if !ecoKnown {
		// Without a known eco temperature the default (17 °C) would be used: require a temperature
		eco = 0
	}

	temp, until, err := parseVacationCommand(payloadStr, eco, time.Now())
	if err != nil {
		slog.Error("Invalid vacation command", "src", deviceID, "payload", payloadStr, "error", err)
		return
	}
	untilStr := until.Format(untilLayout)

	payload := append([]byte{byte(temp*2) | (0x02 << 6)}, encodeUntil(until)...)
	slog.Debug("Vacation encoding", "src", deviceID, "temp", temp, "until", untilStr, "payload", fmt.Sprintf("%X", payload))

	maybeWakeDevice(deviceID)
	if ackTracker != nil {
		ackTracker.TrackVacation(deviceID, temp, untilStr)
	}
	description := fmt.Sprintf("Set Vacation %.1f until %s", temp, untilStr)
	sendCommand(deviceID, 0x40, payload, description)

	if verificationMgr != nil {
		verificationMgr.AddVerification(deviceID, temp, func() {
			slog.Warn("Retrying Set Vacation", "temp", temp, "until", untilStr, "src", deviceID)
			maybeWakeDevice(deviceID)
			sendCommand(deviceID, 0x40, payload, "Retry "+description)
		})
	}
}

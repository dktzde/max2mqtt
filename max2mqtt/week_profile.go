package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// WeekDayProfile matches FHEM/weekprofile JSON style:
// {"time":["06:00","22:00"],"temp":[20.0,17.0]}
type WeekDayProfile struct {
	Time []string  `json:"time"`
	Temp []float64 `json:"temp"`
}

// WeekProfileMQTT is a map of day name -> profile (Mon..Sun or Sat..Fri).
type WeekProfileMQTT map[string]WeekDayProfile

func handleMQTTWeekProfile(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	var profile WeekProfileMQTT
	if err := json.Unmarshal(msg.Payload(), &profile); err != nil {
		slog.Error("Invalid week profile JSON", "error", err)
		return
	}
	slog.Info("Setting week profile", "device", deviceID, "days", len(profile))

	prev := snapshotDevice(deviceID)
	next := prev
	ackTracker.TrackConfig(deviceID, 0x10, "week_profile", prev, next)

	go sendWeekProfile(deviceID, profile)
}

func sendWeekProfile(deviceID string, profile WeekProfileMQTT) {
	for dayName, dayProf := range profile {
		dayNibble, ok := decalcDayToNibble[dayName]
		if !ok {
			// Accept full weekday names
			switch strings.ToLower(dayName) {
			case "saturday":
				dayNibble, ok = 0, true
			case "sunday":
				dayNibble, ok = 1, true
			case "monday":
				dayNibble, ok = 2, true
			case "tuesday":
				dayNibble, ok = 3, true
			case "wednesday":
				dayNibble, ok = 4, true
			case "thursday":
				dayNibble, ok = 5, true
			case "friday":
				dayNibble, ok = 6, true
			}
		}
		if !ok {
			slog.Warn("Unknown weekday in week profile", "day", dayName)
			continue
		}
		part, err := encodeDayProfile(dayProf)
		if err != nil {
			slog.Error("Failed to encode day profile", "day", dayName, "error", err)
			continue
		}
		// FHEM sends two packets: first 7 control points, then remaining 6
		p1 := append([]byte{0x00 | dayNibble}, part[:14]...)
		p2 := append([]byte{0x10 | dayNibble}, part[14:]...)
		sendCommand(deviceID, 0x10, p1, fmt.Sprintf("WeekProfile %s part0", dayName))
		time.Sleep(800 * time.Millisecond)
		sendCommand(deviceID, 0x10, p2, fmt.Sprintf("WeekProfile %s part1", dayName))
		time.Sleep(800 * time.Millisecond)
	}
	// Week profile telegrams are not tracked by the AckTracker: confirming here would confirm an
	// unrelated pending config change of this device. Publish the profile retained.
	topic := fmt.Sprintf("max/%s/week_profile", deviceID)
	if mqttClient != nil {
		mqttClient.Publish(topic, 0, true, mustJSON(profile))
	}
}

func encodeDayProfile(day WeekDayProfile) ([]byte, error) {
	if len(day.Time) != len(day.Temp) {
		return nil, fmt.Errorf("time/temp length mismatch")
	}
	// 13 control points × 2 bytes
	out := make([]byte, 0, 26)
	points := 0
	for i := 0; i < len(day.Time) && points < 13; i++ {
		mins, err := parseHHMM(day.Time[i])
		if err != nil {
			return nil, err
		}
		temp := day.Temp[i]
		word := (uint16(temp*2) << 9) | uint16(mins/5)
		out = append(out, byte(word>>8), byte(word))
		points++
	}
	// Pad remaining control points with 17°C @ end-of-day (0x4520 from FHEM default)
	for points < 13 {
		out = append(out, 0x45, 0x20)
		points++
	}
	return out, nil
}

func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "24:00" {
		return 24 * 60, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return h*60 + m, nil
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func publishWeekProfileDiscovery(srcAddr string) {
	deviceID := "max_" + srcAddr
	// Text entity for advanced JSON week profile (command + state)
	topic := fmt.Sprintf("homeassistant/text/%s_week_profile/config", deviceID)
	payload := map[string]interface{}{
		"name":            "Week Profile JSON",
		"unique_id":       deviceID + "_week_profile",
		"icon":            "mdi:calendar-clock",
		"entity_category": "config",
		"command_topic":   fmt.Sprintf("max/%s/week_profile/set", srcAddr),
		"state_topic":     fmt.Sprintf("max/%s/week_profile", srcAddr),
		"mode":            "text",
		"device":          discoveryDeviceInfo(srcAddr),
	}
	bytes, _ := json.Marshal(payload)
	mqttClient.Publish(topic, 0, true, bytes)
}

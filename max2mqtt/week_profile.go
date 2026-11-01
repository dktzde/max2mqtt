package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Week profile (ConfigWeekProfile 0x10): per day up to 13 control points "temperature until time".
// MAX! devices cannot be asked for their week profile, so the bridge keeps the last one it sent
// (or one imported with max/<id>/programme/import) and publishes it:
//   - max/<id>/programme: whole week as JSON in MAX! Cube format {"monday":[{"temp":18,"until":"07:00"},...],...}
//   - max/<id>/week_profile/<day>: one day in FHEM syntax "17,06:00,21,22:00,17" (text entity per day)
//
// Inputs:
//   - max/<id>/week_profile/<day>/set: one day in FHEM syntax (last temperature until 24:00)
//   - max/<id>/programme/set: {"day":"monday","slots":[{"temp":..,"until":"HH:MM"},...]} (like the
//     MAX! Cube integration's set_programme service) or the whole week in Cube format
//   - max/<id>/programme/import: whole or part of the week in Cube format, stored only (nothing sent)
//   - max/<id>/week_profile/set: {"Mon":{"time":["06:00","24:00"],"temp":[17,21]}} (older JSON format)
// Unchanged days are not sent again.

// WeekDayProfile matches FHEM/weekprofile JSON style:
// {"time":["06:00","22:00"],"temp":[20.0,17.0]}
type WeekDayProfile struct {
	Time []string  `json:"time"`
	Temp []float64 `json:"temp"`
}

// WeekProfileMQTT is a map of day name -> profile (Mon..Sun or Sat..Fri).
type WeekProfileMQTT map[string]WeekDayProfile

// ProgrammeSlot is one control point: temperature until a time of day (MAX! Cube format)
type ProgrammeSlot struct {
	Temp  float64 `json:"temp"`
	Until string  `json:"until"`
}

// Programme is a week profile in MAX! Cube format, keyed by lower-case English day names
type Programme map[string][]ProgrammeSlot

// weekDays in MAX! order: the index is the day number used in ConfigWeekProfile
var weekDays = []string{"saturday", "sunday", "monday", "tuesday", "wednesday", "thursday", "friday"}

const maxControlPoints = 13

var (
	programmeMu sync.Mutex
	programmes  = map[string]Programme{}
)

// dayIndex accepts "Mon", "mon", "monday", "Monday" and returns the MAX! day number
func dayIndex(name string) (int, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for i, d := range weekDays {
		if n == d || (len(n) == 3 && strings.HasPrefix(d, n)) {
			return i, true
		}
	}
	return 0, false
}

// normalizeSlots checks a day: 1-13 points, temperatures 4.5-30.5 in steps of 0.5, times in
// steps of 5 minutes and rising, the last one 24:00. Times are returned as "HH:MM".
func normalizeSlots(slots []ProgrammeSlot) ([]ProgrammeSlot, error) {
	if len(slots) == 0 || len(slots) > maxControlPoints {
		return nil, fmt.Errorf("need 1-%d control points, got %d", maxControlPoints, len(slots))
	}
	out := make([]ProgrammeSlot, 0, len(slots))
	prev := -1
	for i, s := range slots {
		if s.Temp < 4.5 || s.Temp > 30.5 || s.Temp*2 != math.Trunc(s.Temp*2) {
			return nil, fmt.Errorf("temperature %.1f: use 4.5-30.5 in steps of 0.5", s.Temp)
		}
		mins, err := parseHHMM(s.Until)
		if err != nil {
			return nil, err
		}
		if mins%5 != 0 || mins <= prev {
			return nil, fmt.Errorf("time %s: use rising times in steps of 5 minutes", s.Until)
		}
		if i == len(slots)-1 && mins != 24*60 {
			return nil, fmt.Errorf("the last control point must end at 24:00, not %s", s.Until)
		}
		prev = mins
		out = append(out, ProgrammeSlot{Temp: s.Temp, Until: fmt.Sprintf("%02d:%02d", mins/60, mins%60)})
	}
	return out, nil
}

// parseFHEMDay parses "17,06:00,21,22:00,17": temperature, until, ..., last temperature until 24:00
func parseFHEMDay(s string) ([]ProgrammeSlot, error) {
	parts := strings.Split(strings.ReplaceAll(s, " ", ""), ",")
	if len(parts)%2 == 0 {
		return nil, fmt.Errorf("use temp,until,temp,...,temp (last temperature until 24:00)")
	}
	var slots []ProgrammeSlot
	for i := 0; i < len(parts); i += 2 {
		temp, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid temperature %q", parts[i])
		}
		until := "24:00"
		if i+1 < len(parts) {
			until = parts[i+1]
		}
		slots = append(slots, ProgrammeSlot{Temp: temp, Until: until})
	}
	return normalizeSlots(slots)
}

// formatFHEMDay is the inverse of parseFHEMDay
func formatFHEMDay(slots []ProgrammeSlot) string {
	var parts []string
	for i, s := range slots {
		parts = append(parts, strconv.FormatFloat(s.Temp, 'f', -1, 64))
		if i < len(slots)-1 {
			parts = append(parts, s.Until)
		}
	}
	return strings.Join(parts, ",")
}

func slotsEqual(a, b []ProgrammeSlot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// encodeDaySlots packs a day into 13 control points of 2 bytes: temperature*2 in bits 9-15,
// until time in 5-minute steps in bits 0-8; unused points are 17 °C until 24:00 (0x4520, FHEM)
func encodeDaySlots(slots []ProgrammeSlot) []byte {
	out := make([]byte, 0, 2*maxControlPoints)
	for _, s := range slots {
		mins, _ := parseHHMM(s.Until)
		word := (uint16(s.Temp*2) << 9) | uint16(mins/5)
		out = append(out, byte(word>>8), byte(word))
	}
	for len(out) < 2*maxControlPoints {
		out = append(out, 0x45, 0x20)
	}
	return out
}

// sendDaySlots sends one day: control points 1-7 in part 0, points 8-13 in part 1 only when
// the day has more than 7 (as FHEM does), which saves airtime and duty cycle
func sendDaySlots(deviceID string, day int, slots []ProgrammeSlot) {
	data := encodeDaySlots(slots)
	name := weekDays[day]
	unacked := func() { weekProfileDayUnacked(deviceID, day) }
	sendBulkCommand(deviceID, 0x10, append([]byte{byte(day)}, data[:14]...), fmt.Sprintf("WeekProfile %s part0", name), unacked)
	if len(slots) > 7 {
		sendBulkCommand(deviceID, 0x10, append([]byte{0x10 | byte(day)}, data[14:]...), fmt.Sprintf("WeekProfile %s part1", name), unacked)
	}
}

// weekProfileUnackedID is the Home Assistant notification for days a device did not acknowledge
func weekProfileUnackedID(deviceID string) string {
	return "max2mqtt_week_profile_unacked_" + deviceID
}

// weekProfileDayUnacked forgets a day the device did not acknowledge, so the device's profile for
// that day counts as unknown and the next programme/set sends it again instead of skipping it as
// unchanged, and tells Home Assistant.
func weekProfileDayUnacked(deviceID string, day int) {
	name := weekDays[day]
	programmeMu.Lock()
	delete(programmes[deviceID], name)
	programmeMu.Unlock()
	publishProgramme(deviceID)
	slog.Error("Week profile day not acknowledged by the device; it is now unknown, send it again", "device", deviceID, "day", name)
	haNotify(weekProfileUnackedID(deviceID), "MAX! week profile not acknowledged",
		fmt.Sprintf("MAX! device %s did not acknowledge the week profile for %s after %d tries. The day is now unknown to the bridge; send it again (max/%s/programme/set).",
			deviceID, name, bulkMaxTries, deviceID))
}

// setProgramme stores the given days and, if send is set, sends the days that changed
func setProgramme(deviceID string, days map[int][]ProgrammeSlot, send bool) {
	deviceID = strings.ToUpper(deviceID)
	programmeMu.Lock()
	prog := programmes[deviceID]
	if prog == nil {
		prog = Programme{}
		programmes[deviceID] = prog
	}
	var toSend []int
	for day := range weekDays {
		slots, ok := days[day]
		if !ok {
			continue
		}
		if send {
			if old, known := prog[weekDays[day]]; known && slotsEqual(old, slots) {
				slog.Info("Week profile unchanged, not sent", "device", deviceID, "day", weekDays[day])
			} else {
				toSend = append(toSend, day)
			}
		}
		prog[weekDays[day]] = slots
	}
	programmeMu.Unlock()

	for _, day := range toSend {
		sendDaySlots(deviceID, day, days[day])
	}
	if !send {
		slog.Info("Imported week profile (not sent)", "device", deviceID, "days", len(days))
	}
	publishProgramme(deviceID)
}

// publishProgramme publishes the stored week profile (whole week and per day), retained
func publishProgramme(deviceID string) {
	if mqttClient == nil {
		return
	}
	programmeMu.Lock()
	prog := Programme{}
	for d, s := range programmes[deviceID] {
		prog[d] = append([]ProgrammeSlot(nil), s...)
	}
	programmeMu.Unlock()
	mqttClient.Publish(fmt.Sprintf("max/%s/programme", deviceID), 0, true, mustJSON(prog))
	for d, slots := range prog {
		mqttClient.Publish(fmt.Sprintf("max/%s/week_profile/%s", deviceID, d), 0, true, formatFHEMDay(slots))
	}
	publishAttributes(deviceID)
}

// parseProgrammePayload reads {"day":"monday","slots":[...]} or {"monday":[...],...}
func parseProgrammePayload(payload []byte) (map[int][]ProgrammeSlot, error) {
	var single struct {
		Day   string          `json:"day"`
		Slots []ProgrammeSlot `json:"slots"`
	}
	if err := json.Unmarshal(payload, &single); err == nil && single.Day != "" {
		day, ok := dayIndex(single.Day)
		if !ok {
			return nil, fmt.Errorf("unknown day %q", single.Day)
		}
		slots, err := normalizeSlots(single.Slots)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", single.Day, err)
		}
		return map[int][]ProgrammeSlot{day: slots}, nil
	}
	var week map[string][]ProgrammeSlot
	if err := json.Unmarshal(payload, &week); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	days := map[int][]ProgrammeSlot{}
	for name, s := range week {
		day, ok := dayIndex(name)
		if !ok {
			return nil, fmt.Errorf("unknown day %q", name)
		}
		slots, err := normalizeSlots(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		days[day] = slots
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("no days given")
	}
	return days, nil
}

func handleMQTTProgrammeSet(client mqtt.Client, msg mqtt.Message) {
	handleProgrammePayload(msg, true)
}

func handleMQTTProgrammeImport(client mqtt.Client, msg mqtt.Message) {
	handleProgrammePayload(msg, false)
}

func handleProgrammePayload(msg mqtt.Message, send bool) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	days, err := parseProgrammePayload(msg.Payload())
	if err != nil {
		slog.Error("Invalid programme", "topic", msg.Topic(), "error", err)
		return
	}
	setProgramme(deviceID, days, send)
}

// handleMQTTWeekProfileDay handles max/<id>/week_profile/<day>/set in FHEM syntax
func handleMQTTWeekProfileDay(client mqtt.Client, msg mqtt.Message) {
	parts := strings.Split(msg.Topic(), "/")
	if len(parts) < 5 {
		return
	}
	day, ok := dayIndex(parts[3])
	if !ok {
		slog.Error("Unknown weekday in topic", "topic", msg.Topic())
		return
	}
	slots, err := parseFHEMDay(string(msg.Payload()))
	if err != nil {
		slog.Error("Invalid week profile day", "topic", msg.Topic(), "payload", string(msg.Payload()), "error", err)
		publishProgramme(strings.ToUpper(parts[1]))
		return
	}
	setProgramme(parts[1], map[int][]ProgrammeSlot{day: slots}, true)
}

// handleMQTTWeekProfile handles the older JSON format on max/<id>/week_profile/set
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
	days := map[int][]ProgrammeSlot{}
	for dayName, dayProf := range profile {
		day, ok := dayIndex(dayName)
		if !ok {
			slog.Warn("Unknown weekday in week profile", "day", dayName)
			continue
		}
		if len(dayProf.Time) != len(dayProf.Temp) {
			slog.Error("Failed to encode day profile", "day", dayName, "error", "time/temp length mismatch")
			continue
		}
		var slots []ProgrammeSlot
		for i := range dayProf.Time {
			slots = append(slots, ProgrammeSlot{Temp: dayProf.Temp[i], Until: dayProf.Time[i]})
		}
		norm, err := normalizeSlots(slots)
		if err != nil {
			slog.Error("Failed to encode day profile", "day", dayName, "error", err)
			continue
		}
		days[day] = norm
	}
	if len(days) > 0 {
		setProgramme(deviceID, days, true)
	}
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
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
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
	// The former single "Week Profile JSON" text entity could not hold a week (Home Assistant
	// limits text states to 255 characters): remove it, one text entity per day instead
	mqttClient.Publish(fmt.Sprintf("homeassistant/text/%s_week_profile/config", deviceID), 0, true, "")
	for _, day := range weekDays {
		topic := fmt.Sprintf("homeassistant/text/%s_week_profile_%s/config", deviceID, day)
		payload := map[string]interface{}{
			"name":            "Week Profile " + strings.ToUpper(day[:1]) + day[1:],
			"unique_id":       deviceID + "_week_profile_" + day,
			"icon":            "mdi:calendar-clock",
			"entity_category": "config",
			"command_topic":   fmt.Sprintf("max/%s/week_profile/%s/set", srcAddr, day),
			"state_topic":     fmt.Sprintf("max/%s/week_profile/%s", srcAddr, day),
			"max":             255,
			"mode":            "text",
			"device":          discoveryDeviceInfo(srcAddr),
		}
		publishDiscovery(topic, payload)
	}
}

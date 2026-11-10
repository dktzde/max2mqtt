package main

import (
	"strings"
	"testing"
	"time"
)

// resetWakeUpState forgets when devices were heard or woken, so tests do not depend on each other
func resetWakeUpState(t *testing.T) {
	t.Helper()
	lastSeenMu.Lock()
	lastSeenDevice = map[string]time.Time{}
	lastWakeUpQueued = map[string]time.Time{}
	lastSeenMu.Unlock()
}

// txType returns the message type byte of a queued CUL command "Zs<len><cnt><flag><type>..."
func txType(t *testing.T, cmd string) string {
	t.Helper()
	if !strings.HasPrefix(cmd, "Zs") || len(cmd) < 10 {
		t.Fatalf("not a CUL send command: %q", cmd)
	}
	return cmd[8:10]
}

func txTypes(t *testing.T, cmds []string) string {
	t.Helper()
	types := make([]string, 0, len(cmds))
	for _, c := range cmds {
		types = append(types, txType(t, c))
	}
	return strings.Join(types, " ")
}

const wakeTestDevice = "0A1B2C"

func importKnownConfig(t *testing.T, dev string) {
	t.Helper()
	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{
		"comfort_temp": 16, "eco_temp": 12, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30,
		"boost_duration": 5, "boost_valve_position": 40, "decalc_day": "sat", "decalc_hour": 9,
		"max_valve_setting": 100, "valve_offset": 0}`})
	if tx := pendingTX(); len(tx) != 0 {
		t.Fatalf("import must not send: %v", tx)
	}
}

// Seen on the device 2026-11-10: config valve, then WakeUp, then boost. The WakeUp reached the
// device while it answered the config and was lost, the boost was not acknowledged either.
func TestWakeUpGoesBeforeConfigAndBoost(t *testing.T) {
	resetTestState()
	resetWakeUpState(t)
	initAckTracker()
	importKnownConfig(t, wakeTestDevice)

	handleMQTTConfigSet(nil, fakeMessage{topic: "max/" + wakeTestDevice + "/config/set",
		payload: `{"boost_duration": 30, "boost_valve_position": 25, "decalc_day": "Sat", "decalc_hour": 9, "max_valve_setting": 100, "valve_offset": 0}`})
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + wakeTestDevice + "/preset/set", payload: "boost"})

	if got := txTypes(t, pendingTX()); got != "f1 12 40" {
		t.Fatalf("telegram types %q, want WakeUp, config valve, SetTemperature (f1 12 40)", got)
	}
}

func TestOneWakeUpForConfigTemperaturesAndValve(t *testing.T) {
	resetTestState()
	resetWakeUpState(t)
	initAckTracker()
	importKnownConfig(t, wakeTestDevice)

	handleMQTTConfigSet(nil, fakeMessage{topic: "max/" + wakeTestDevice + "/config/set",
		payload: `{
			"comfort_temp": 16, "eco_temp": 13, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
			"window_open_temp": 7, "window_open_duration": 30,
			"boost_duration": 30, "boost_valve_position": 25, "decalc_day": "Sat", "decalc_hour": 9,
			"max_valve_setting": 100, "valve_offset": 0}`})

	if got := txTypes(t, pendingTX()); got != "f1 11 12" {
		t.Fatalf("telegram types %q, want one WakeUp, then both config telegrams (f1 11 12)", got)
	}
}

func TestNoWakeUpForADeviceJustHeard(t *testing.T) {
	resetTestState()
	resetWakeUpState(t)
	initAckTracker()
	importKnownConfig(t, wakeTestDevice)
	touchLastSeen(wakeTestDevice)

	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + wakeTestDevice + "/boost_duration/set", payload: "10"})

	if got := txTypes(t, pendingTX()); got != "12" {
		t.Fatalf("telegram types %q, want only the config valve telegram", got)
	}
}

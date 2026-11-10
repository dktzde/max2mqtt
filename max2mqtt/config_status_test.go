package main

import (
	"encoding/json"
	"testing"
)

// countersByType returns the message counter of each queued telegram by its type ("11", "12", ...)
func countersByType(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, c := range pendingTX() {
		out[txType(t, c)] = cmdCounter(t, c)
	}
	return out
}

func configStatusAttr(t *testing.T, fc *fakeMQTT, deviceID string) map[string]ConfigStatus {
	t.Helper()
	var attrs struct {
		ConfigStatus map[string]ConfigStatus `json:"config_status"`
	}
	raw := fc.get("max/" + deviceID + "/attributes")
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		t.Fatalf("attributes %q: %v", raw, err)
	}
	return attrs.ConfigStatus
}

func resetConfigStatus(t *testing.T) {
	t.Helper()
	configStatusMu.Lock()
	configStatus = map[string]map[string]ConfigStatus{}
	configStatusMu.Unlock()
}

// config/set writes temperatures and valve: each change is acknowledged on its own
func TestConfigStatusPerKind(t *testing.T) {
	setupAckCounterTest(t)
	fc := mqttClient.(*fakeMQTT)
	resetWakeUpState(t)
	resetConfigStatus(t)
	importKnownConfig(t, ackTestDevice)

	handleMQTTConfigSet(nil, fakeMessage{topic: "max/" + ackTestDevice + "/config/set", payload: `{
		"comfort_temp": 16, "eco_temp": 13, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30,
		"boost_duration": 30, "boost_valve_position": 25, "decalc_day": "Sat", "decalc_hour": 9,
		"max_valve_setting": 100, "valve_offset": 0}`})
	cnt := countersByType(t)
	st := configStatusAttr(t, fc, ackTestDevice)
	if st["temperatures"].State != statusPending || st["valve"].State != statusPending || st["valve"].Field != "config_valve" {
		t.Fatalf("after sending: %+v", st)
	}

	receiveShortAck(ackTestDevice, ackTestGateway, cnt["12"])
	st = configStatusAttr(t, fc, ackTestDevice)
	if st["valve"].State != statusAcknowledged || st["temperatures"].State != statusPending {
		t.Fatalf("after the valve Ack: %+v", st)
	}
	if st["valve"].At == "" {
		t.Fatal("no time in the valve status")
	}

	receiveShortAck(ackTestDevice, ackTestGateway, cnt["11"])
	if st = configStatusAttr(t, fc, ackTestDevice); st["temperatures"].State != statusAcknowledged {
		t.Fatalf("after the temperatures Ack: %+v", st)
	}
}

// Without the per-kind tracking the valve change replaced the pending temperature change: the
// temperatures could neither be confirmed nor rolled back, and a rollback restored both kinds
func TestUnackedValveKeepsPendingTemperatures(t *testing.T) {
	setupAckCounterTest(t)
	fc := mqttClient.(*fakeMQTT)
	resetWakeUpState(t)
	resetConfigStatus(t)
	importKnownConfig(t, ackTestDevice)

	handleMQTTConfigSet(nil, fakeMessage{topic: "max/" + ackTestDevice + "/config/set", payload: `{
		"comfort_temp": 16, "eco_temp": 13, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30,
		"boost_duration": 30, "boost_valve_position": 25, "decalc_day": "Sat", "decalc_hour": 9,
		"max_valve_setting": 100, "valve_offset": 0}`})
	cnt := countersByType(t)

	if !ackTracker.ConfigUnacked(ackTestDevice, cnt["12"]) {
		t.Fatal("valve change not found by its counter")
	}
	if d := boostDurationOf(ackTestDevice); d != 5 {
		t.Fatalf("boost duration %d, want the previous 5", d)
	}
	cfg := getDeviceConfig(ackTestDevice)
	configMutex.RLock()
	eco := cfg.EcoTemp
	configMutex.RUnlock()
	if eco != 13 {
		t.Fatalf("eco temperature %.1f: the valve rollback changed the pending temperatures", eco)
	}
	st := configStatusAttr(t, fc, ackTestDevice)
	if st["valve"].State != statusNotAcknowledged || st["temperatures"].State != statusPending {
		t.Fatalf("status %+v", st)
	}

	receiveShortAck(ackTestDevice, ackTestGateway, cnt["11"])
	if st = configStatusAttr(t, fc, ackTestDevice); st["temperatures"].State != statusAcknowledged {
		t.Fatalf("temperatures not confirmed: %+v", st)
	}
}

func TestReplacedConfigChangeIsNotRolledBack(t *testing.T) {
	setupAckCounterTest(t)
	resetWakeUpState(t)
	resetConfigStatus(t)
	importKnownConfig(t, ackTestDevice)
	touchLastSeen(ackTestDevice)

	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + ackTestDevice + "/boost_duration/set", payload: "10"})
	first := countersByType(t)["12"]
	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + ackTestDevice + "/boost_duration/set", payload: "30"})
	second := countersByType(t)["12"]

	// The first change was replaced: giving up on its telegram must not undo the second one
	if ackTracker.ConfigUnacked(ackTestDevice, first) {
		t.Fatal("the replaced change was rolled back")
	}
	if d := boostDurationOf(ackTestDevice); d != 30 {
		t.Fatalf("boost duration %d, want 30", d)
	}
	receiveShortAck(ackTestDevice, ackTestGateway, second)
	if configPending(ackTestDevice) {
		t.Fatal("the second change was not confirmed")
	}
}

package main

import (
	"strings"
	"testing"
)

func pendingTX() []string {
	var out []string
	for len(serialWrite) > 0 {
		out = append(out, <-serialWrite)
	}
	return out
}

func TestSingleConfigValueNeedsKnownValues(t *testing.T) {
	resetTestState()
	initAckTracker()
	const dev = "0A1B2C"

	// Unknown values: a single change must not send defaults for the other six
	handleMQTTEcoTemp(nil, fakeMessage{topic: "max/" + dev + "/eco_temp/set", payload: "16"})
	if tx := pendingTX(); len(tx) != 0 {
		t.Fatalf("sent with unknown values: %v", tx)
	}
	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + dev + "/boost_duration/set", payload: "10"})
	if tx := pendingTX(); len(tx) != 0 {
		t.Fatalf("valve sent with unknown values: %v", tx)
	}
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "eco"})
	if tx := pendingTX(); len(tx) != 0 {
		t.Fatalf("eco preset sent with unknown eco temperature: %v", tx)
	}

	// Incomplete import is refused
	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{"eco_temp": 12}`})
	if getDeviceConfig(dev).TempsKnown {
		t.Fatal("incomplete import accepted")
	}

	// Import the real values (no TX), then change one: all others keep the imported values
	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{
		"comfort_temp": 16, "eco_temp": 12, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30,
		"boost_duration": 15, "boost_valve_position": 100, "decalc_day": "sat", "decalc_hour": 9,
		"max_valve_setting": 100, "valve_offset": 0}`})
	if tx := pendingTX(); len(tx) != 0 {
		t.Fatalf("import must not send: %v", tx)
	}
	handleMQTTEcoTemp(nil, fakeMessage{topic: "max/" + dev + "/eco_temp/set", payload: "13"})
	tx := pendingTX()
	// 0x11 payload: comfort 32, eco 26, max 61, min 09, offset 07, window 0E, duration 06
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "201a3d09070e06") {
		t.Fatalf("config temperatures %v", tx)
	}
	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + dev + "/boost_duration/set", payload: "10"})
	tx = pendingTX()
	// 0x12: boost 10 min (2<<5) | 100/5 = 0x54, decalc Sat 9 = 0x09, max valve 0xFF, offset 0
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "5409ff00") {
		t.Fatalf("config valve %v", tx)
	}
}

func TestConfigEntitiesUnknownUntilKnown(t *testing.T) {
	fc := resetTestState()
	publishConfigEntities("0A1B2C")
	cfg := fc.get("homeassistant/number/max_0A1B2C_eco_temp/config")
	if !strings.Contains(cfg, "if value_json.temps_known else None") {
		t.Fatalf("eco entity template: %s", cfg)
	}
}

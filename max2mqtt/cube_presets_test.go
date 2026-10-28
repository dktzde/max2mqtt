package main

import (
	"strings"
	"testing"
)

func lastTX(t *testing.T) string {
	t.Helper()
	var last string
	for len(serialWrite) > 0 {
		if c := <-serialWrite; !strings.HasPrefix(c, "Zs0b") || !strings.HasSuffix(c, "3f") || len(c) != 2+24 {
			last = c
		}
	}
	return last
}

func importSchlafenConfig(dev string) {
	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{
		"comfort_temp": 16, "eco_temp": 12, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30}`})
}

func TestCubePresetsFromStatus(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	importSchlafenConfig(dev)
	cases := []struct {
		setpoint byte
		want     string
	}{{32, "comfort"}, {24, "eco"}, {14, "Window Open"}, {61, "On"}, {26, "none"}}
	for _, c := range cases {
		updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 0, c.setpoint, 0, 140}, true))
		if !strings.Contains(fc.get("max/"+dev+"/state"), `"preset_mode":"`+c.want+`"`) {
			t.Errorf("setpoint %.1f: want %s, state %s", float64(c.setpoint)/2, c.want, fc.get("max/"+dev+"/state"))
		}
	}
	sendDiscovery(dev)
	if !strings.Contains(fc.get("homeassistant/climate/max_"+dev+"/config"), `"preset_modes":["comfort","eco","Window Open","On","boost"]`) {
		t.Fatalf("discovery: %s", fc.get("homeassistant/climate/max_"+dev+"/config"))
	}
}

func TestCubePresetCommands(t *testing.T) {
	resetTestState()
	const dev = "0A1B2C"
	importSchlafenConfig(dev)
	touchLastSeen(dev)

	// Auto 13 °C: eco keeps auto (auto bits 00 | 12 °C = 0x18)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 0, 26, 0, 140}, true))
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "eco"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "18") {
		t.Fatalf("eco in auto: %s", tx)
	}
	// Window Open (7 °C = 0x0E), On (30.5 °C = 0x3D)
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "Window Open"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "0e") {
		t.Fatalf("window open: %s", tx)
	}
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "On"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "3d") {
		t.Fatalf("on: %s", tx)
	}

	// Manual 6.5 °C, boost pressed on the device, then preset none from HA:
	// back to manual 6.5 °C (0x4D), not auto
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, 13, 0, 200}, true))
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x03, 100, 13, 0, 200}, true))
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "none"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "4d") {
		t.Fatalf("none after boost must restore manual 6.5: %s", tx)
	}

	// Boost from HA while in auto, then none: back to auto with the week program (0x00)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 0, 26, 0, 140}, true))
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "boost"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "c0") {
		t.Fatalf("boost: %s", tx)
	}
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "none"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "00") {
		t.Fatalf("none after HA boost from auto: %s", tx)
	}

	// none without boost: auto, week program (like the Cube integration)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, 40, 0, 200}, true))
	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "none"})
	if tx := lastTX(t); !strings.HasSuffix(tx, "00") {
		t.Fatalf("none in manual: %s", tx)
	}
}

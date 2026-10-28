package main

import (
	"strings"
	"testing"
)

func TestPresetFollowsDeviceStatus(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	state := func() string { return fc.get("max/" + dev + "/state") }

	// Boost pressed on the device, then the boost ends (device back in auto)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x03, 100, 26, 0, 140}, true))
	if !strings.Contains(state(), `"preset_mode":"boost"`) {
		t.Fatalf("boost not reported: %s", state())
	}
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 140}, true))
	if !strings.Contains(state(), `"preset_mode":"none"`) {
		t.Fatalf("preset still set after boost ended: %s", state())
	}

	// Eco set from HA stays while the device reports manual at the eco temperature
	stateMutex.Lock()
	stateCache[dev].PresetMode = "eco"
	stateMutex.Unlock()
	eco := byte(getDeviceConfig(dev).EcoTemp * 2)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, eco, 0, 140}, true))
	if !strings.Contains(state(), `"preset_mode":"eco"`) {
		t.Fatalf("eco lost: %s", state())
	}
	// Wheel turned on the device: other temperature -> none
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, eco + 2, 0, 140}, true))
	if !strings.Contains(state(), `"preset_mode":"none"`) {
		t.Fatalf("eco kept after setpoint change: %s", state())
	}
}

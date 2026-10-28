package main

import (
	"strings"
	"testing"
)

func TestZeroActualTemperatureIgnored(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 140}))
	if d := decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 0}); d.CurrentTemperature != nil {
		t.Fatalf("0 °C must not be decoded as actual temperature")
	}
	if d := decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 3}); d.CurrentTemperature != nil {
		t.Fatalf("0.3 °C must not be decoded as actual temperature")
	}
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 0}))
	state := fc.get("max/" + dev + "/state")
	if !strings.Contains(state, `"current_temperature":14`) || !strings.Contains(state, `"hvac_action":"idle"`) {
		t.Fatalf("last actual temperature lost: %s", state)
	}
}

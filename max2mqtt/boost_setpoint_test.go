package main

import (
	"strings"
	"testing"
)

// drainTX returns the Zs commands queued on the serial channel (no TX manager in tests)
func drainTX() []string {
	var out []string
	for {
		select {
		case c := <-serialWrite:
			out = append(out, c)
		default:
			return out
		}
	}
}

func TestBoostKeepsSetpoint(t *testing.T) {
	resetTestState()
	const dev = "0A1B2C"
	// Manual 6.5 °C
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, 13, 0, 200}))
	touchLastSeen(dev)

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + dev + "/preset/set", payload: "boost"})
	tx := drainTX()
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "c0") {
		t.Fatalf("boost must be sent as mode 3 with temperature 0 (0xC0): %v", tx)
	}
	if got := stateCache[dev].Temperature; got != 6.5 {
		t.Fatalf("boost overwrote cached setpoint: %.1f", got)
	}

	// Back to heat: manual with the previous 6.5 °C (0x40 | 13 = 0x4D), not 30 °C
	handleMQTTModeCommand(nil, fakeMessage{topic: "max/" + dev + "/mode/set", payload: "heat"})
	tx = drainTX()
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "4d") {
		t.Fatalf("heat after boost must restore 6.5 °C manual (0x4D): %v", tx)
	}

	// Boost via hvac mode topic is also sent with temperature 0
	handleMQTTModeCommand(nil, fakeMessage{topic: "max/" + dev + "/mode/set", payload: "boost"})
	tx = drainTX()
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "c0") {
		t.Fatalf("mode boost must be 0xC0: %v", tx)
	}
}

package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestAutoFollowsWeekProgram(t *testing.T) {
	resetTestState()
	initAckTracker()
	const dev = "0A1B2C"
	// Manual 21 °C
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x01, 0, 42, 0, 200}, true))
	touchLastSeen(dev)

	handleMQTTModeCommand(nil, fakeMessage{topic: "max/" + dev + "/mode/set", payload: "auto"})
	var tx []string
	for len(serialWrite) > 0 {
		tx = append(tx, <-serialWrite)
	}
	if len(tx) != 1 || !strings.HasSuffix(tx[0], "00") {
		t.Fatalf("auto must be sent with temperature 0 (week program): %v", tx)
	}

	// Short Ack for the SetTemperature telegram (same counter): mode auto applied, setpoint not set to 0
	cnt, err := strconv.ParseUint(tx[0][4:6], 16, 8) // "Zs" + length byte, then the counter
	if err != nil {
		t.Fatal(err)
	}
	ackTracker.ApplySetpointAck(dev, int(cnt))
	if d := stateCache[dev]; d.Mode != "auto" || d.Temperature != 21 {
		t.Fatalf("after Ack: mode %s temp %.1f", d.Mode, d.Temperature)
	}
}

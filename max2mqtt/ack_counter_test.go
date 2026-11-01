package main

import (
	"encoding/hex"
	"fmt"
	"testing"
)

const (
	ackTestGateway = "0A0B0C"
	ackTestDevice  = "1A2B3C"
	ackTestOther   = "1D2E3F"
)

// setupAckCounterTest gives the test a fake MQTT client, a fresh AckTracker and a gateway address
func setupAckCounterTest(t *testing.T) {
	t.Helper()
	resetTestState()
	oldGateway := config.GatewayID
	config.GatewayID = ackTestGateway
	initAckTracker()
	t.Cleanup(func() {
		config.GatewayID = oldGateway
		ackTracker = nil
	})
}

// sentCounter returns the message counter of the next telegram written to the CUL
func sentCounter(t *testing.T) int {
	t.Helper()
	select {
	case cmd := <-serialWrite:
		data, err := hex.DecodeString(cmd[2:]) // strip "Zs"
		if err != nil || len(data) < 2 {
			t.Fatalf("invalid CUL command %q", cmd)
		}
		return int(data[1])
	default:
		t.Fatal("no telegram was sent")
		return 0
	}
}

// receiveShortAck feeds a short Ack (payload 0x01) from src to dst with the given counter
func receiveShortAck(src, dst string, cnt int) {
	handleSerialMessage(fmt.Sprintf("Z0B%02X0002%s%s0001", cnt, src, dst))
}

func configPending(deviceID string) bool {
	ackTracker.mutex.Lock()
	defer ackTracker.mutex.Unlock()
	_, ok := ackTracker.pending[deviceID]
	return ok
}

func setpointPending(deviceID string) bool {
	ackTracker.mutex.Lock()
	defer ackTracker.mutex.Unlock()
	_, ok := ackTracker.pendingSetTemp[deviceID]
	return ok
}

func TestConfigAckNeedsMatchingCounter(t *testing.T) {
	setupAckCounterTest(t)
	prev := snapshotDevice(ackTestDevice)
	next := prev
	next.Temps.MeasurementOffset = 1.5
	ackTracker.TrackConfig(ackTestDevice, 0x11, "measurement_offset", prev, next)
	sendCommand(ackTestDevice, 0x11, []byte{0x28, 0x18, 0x3D, 0x09, 0x0A, 0x10, 0x04}, "Config Temps")
	cnt := sentCounter(t)

	// Ack for the telegram sent before (e.g. the last week profile day) must not confirm
	receiveShortAck(ackTestDevice, ackTestGateway, cnt-1)
	if !configPending(ackTestDevice) {
		t.Fatal("Ack with another counter confirmed the config change")
	}

	receiveShortAck(ackTestDevice, ackTestGateway, cnt)
	if configPending(ackTestDevice) {
		t.Fatal("Ack with the matching counter did not confirm the config change")
	}
}

func TestAckToOtherDeviceConfirmsNothing(t *testing.T) {
	setupAckCounterTest(t)
	prev := snapshotDevice(ackTestDevice)
	next := prev
	next.Display = "Actual"
	ackTracker.TrackConfig(ackTestDevice, 0x82, "display_mode", prev, next)
	sendCommand(ackTestDevice, 0x82, []byte{0x04}, "Set Display Mode: Actual")
	cnt := sentCounter(t)

	// The device also Acks telegrams from linked devices (window contact, other thermostats)
	receiveShortAck(ackTestDevice, ackTestOther, cnt)
	if !configPending(ackTestDevice) {
		t.Fatal("Ack to another device confirmed the config change")
	}

	receiveShortAck(ackTestDevice, ackTestGateway, cnt)
	if configPending(ackTestDevice) {
		t.Fatal("Ack to the gateway did not confirm the config change")
	}
}

func TestRemoveGroupIDAckConfirmsGroupChange(t *testing.T) {
	setupAckCounterTest(t)
	prev := snapshotDevice(ackTestDevice)
	next := prev
	next.GroupID = 0
	ackTracker.TrackConfig(ackTestDevice, 0x22, "group_id", prev, next)
	sendCommand(ackTestDevice, 0x23, []byte{0x00}, "Remove Group ID")
	cnt := sentCounter(t)

	receiveShortAck(ackTestDevice, ackTestGateway, cnt)
	if configPending(ackTestDevice) {
		t.Fatal("Ack for RemoveGroupID did not confirm the group change")
	}
}

func TestSetpointAckIgnoresWakeUpAck(t *testing.T) {
	setupAckCounterTest(t)
	sendCommand(ackTestDevice, 0xF1, []byte{0x3F}, "WakeUp")
	wakeCnt := sentCounter(t)
	ackTracker.TrackSetpoint(ackTestDevice, 21, "manual", "heat")
	sendCommand(ackTestDevice, 0x40, []byte{0x6A}, "Set Temp 21.0")
	setCnt := sentCounter(t)

	receiveShortAck(ackTestDevice, ackTestGateway, wakeCnt)
	if !setpointPending(ackTestDevice) {
		t.Fatal("Ack for the WakeUp applied the pending setpoint")
	}

	receiveShortAck(ackTestDevice, ackTestGateway, setCnt)
	if setpointPending(ackTestDevice) {
		t.Fatal("Ack for SetTemperature did not apply the pending setpoint")
	}
	stateMutex.Lock()
	got := stateCache[ackTestDevice].Temperature
	stateMutex.Unlock()
	if got != 21 {
		t.Fatalf("temperature = %v, want 21", got)
	}
}

func TestSetpointAckAcceptsRetryCounter(t *testing.T) {
	setupAckCounterTest(t)
	ackTracker.TrackSetpoint(ackTestDevice, 19, "manual", "heat")
	sendCommand(ackTestDevice, 0x40, []byte{0x66}, "Set Temp 19.0")
	firstCnt := sentCounter(t)
	sendCommand(ackTestDevice, 0x40, []byte{0x66}, "Retry Set Temp 19.0")
	sentCounter(t)

	// The late Ack of the first try still counts
	receiveShortAck(ackTestDevice, ackTestGateway, firstCnt)
	if setpointPending(ackTestDevice) {
		t.Fatal("Ack for the first SetTemperature try did not apply the setpoint")
	}
}

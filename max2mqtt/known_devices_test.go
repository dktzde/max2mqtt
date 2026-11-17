package main

import (
	"testing"
)

// thermostat state 0x60 from src to the broadcast address (auto, valve 0 %, 19 °C, actual 20.1 °C)
func radiatorState(src, dst string) string {
	return "Z100004" + "60" + src + dst + "00" + "1800260000C9"
}

func resetKnownDevices(t *testing.T) {
	t.Helper()
	pairedMu.Lock()
	oldPaired, oldLogged := pairedDevices, unknownLogged
	pairedDevices = map[string]bool{}
	unknownLogged = map[string]bool{}
	pairedMu.Unlock()
	old := config.AcceptUnknownDevices
	t.Cleanup(func() {
		config.AcceptUnknownDevices = old
		pairedMu.Lock()
		pairedDevices, unknownLogged = oldPaired, oldLogged
		pairedMu.Unlock()
	})
}

func known(id string) bool {
	stateMutex.RLock()
	defer stateMutex.RUnlock()
	_, ok := stateCache[id]
	return ok
}

func TestUnknownDeviceIsNotCreated(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	handleSerialMessage(radiatorState(ackTestDevice, "000000"))
	if known(ackTestDevice) {
		t.Fatal("device created from a broadcast of an unknown device")
	}
	handleSerialMessage(radiatorState(ackTestDevice, ackTestOther))
	if known(ackTestDevice) {
		t.Fatal("device created from a telegram to another device")
	}
}

func TestDeviceCreatedFromTelegramToGateway(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	handleSerialMessage(radiatorState(ackTestDevice, ackTestGateway))
	if !known(ackTestDevice) {
		t.Fatal("device not created from a telegram to the gateway")
	}
	// known now: its broadcasts are handled too
	handleSerialMessage(radiatorState(ackTestDevice, "000000"))
	if !known(ackTestDevice) {
		t.Fatal("known device lost")
	}
}

func TestPairedDeviceIsCreated(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	markPaired(ackTestDevice)
	handleSerialMessage(radiatorState(ackTestDevice, "000000"))
	if !known(ackTestDevice) {
		t.Fatal("paired device not created")
	}
}

func TestAcceptUnknownDevicesOption(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	config.AcceptUnknownDevices = true
	handleSerialMessage(radiatorState(ackTestDevice, "000000"))
	if !known(ackTestDevice) {
		t.Fatal("device not created with accept_unknown_devices")
	}
}

// Answering a time request of a foreign device must not make it known (no config created)
func TestTimeRequestDoesNotAdmitUnknownDevice(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	handleSerialMessage("Z0A000403" + ackTestDevice + "000000" + "00")
	handleSerialMessage(radiatorState(ackTestDevice, "000000"))
	if known(ackTestDevice) || isKnownDevice(ackTestDevice) {
		t.Fatal("time request admitted an unknown device")
	}
}

// A PairPong that could not be queued does not admit the device
func TestFailedPairPongDoesNotAdmit(t *testing.T) {
	setupAckCounterTest(t)
	resetKnownDevices(t)
	old := serialWrite
	serialWrite = make(chan string) // no reader: full
	t.Cleanup(func() { serialWrite = old })
	sendPairPong(ackTestDevice)
	if isKnownDevice(ackTestDevice) {
		t.Fatal("device admitted although the PairPong was not sent")
	}
}

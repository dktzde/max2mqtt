package main

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Commands to a device wait for the device's Ack and are sent again without one (see
// TransmissionManager.EnqueueAcked). Seen on a radiator thermostat 2026-11-10: a boost sent right
// after a config telegram got no Ack, nothing sent it again, and Home Assistant showed the boost
// for two seconds, then the old mode.

// commandAckTries returns how often a telegram of this type to dstAddr is sent without an Ack;
// 0 = the telegram does not wait for an Ack. Broadcasts and telegrams a device does not answer
// with an Ack to the gateway (time information, factory reset) do not wait.
func commandAckTries(dstAddr string, typeByte byte) int {
	if dstAddr == "000000" {
		return 0
	}
	switch typeByte {
	case 0x40, // SetTemperature: setpoint, mode, preset, boost, vacation
		0x11, 0x12, // ConfigTemperatures, ConfigValve
		0x20, 0x21, // AddLinkPartner, RemoveLinkPartner
		0x22, 0x23, // SetGroupId, RemoveGroupId
		0x82: // SetDisplayActualTemperature
		return bulkMaxTries
	case 0xF1:
		// WakeUp: waiting for its Ack keeps the next telegram from reaching the device while it is
		// still answering; it is not sent again, the next telegram has its own wakeup preamble
		return 1
	}
	return 0
}

// commandUnackedID is the Home Assistant notification for a command the device did not acknowledge
func commandUnackedID(deviceID string) string {
	return "max2mqtt_command_unacked_" + deviceID
}

var (
	commandUnackedMu       sync.Mutex
	commandUnackedNotified = map[string]bool{}
)

// commandAckCallbacks returns what happens after the device acknowledged a command, after the
// last try without an Ack, and when the command was dropped without being sent (no duty cycle
// credits until command_timeout).
func commandAckCallbacks(deviceID string, typeByte byte, cnt int, description string, tries int) (acked, unacked, dropped func()) {
	acked = func() { commandAcked(deviceID) }
	if typeByte == 0xF1 {
		return acked, nil, nil
	}
	undo := func() {
		switch typeByte {
		case 0x40:
			// back to the state from before the command, unless the device reported one since
			ackTracker.SetpointUnacked(deviceID, cnt)
		case 0x11, 0x12, 0x22, 0x23, 0x82:
			ackTracker.ConfigUnacked(deviceID, cnt)
		}
	}
	unacked = func() {
		undo()
		commandUnacked(deviceID, fmt.Sprintf("did not acknowledge \"%s\" after %d tries", description, tries))
	}
	dropped = func() {
		undo()
		commandUnacked(deviceID, fmt.Sprintf("did not get \"%s\": it was not sent, the duty cycle credits did not suffice within command_timeout", description))
	}
	return acked, unacked, dropped
}

// commandUnacked tells Home Assistant that a command did not reach the device; what says what
// happened, after the device ID
func commandUnacked(deviceID, what string) {
	commandUnackedMu.Lock()
	commandUnackedNotified[deviceID] = true
	commandUnackedMu.Unlock()
	haNotify(commandUnackedID(deviceID), "MAX! command not acknowledged",
		fmt.Sprintf("MAX! device %s %s (%s). Home Assistant shows the last state the device reported; send the command again. This notification disappears after the next acknowledged command to this device.",
			deviceID, what, time.Now().Format("15:04")))
}

// commandAcked removes the notification of an earlier unacknowledged command to the device
func commandAcked(deviceID string) {
	commandUnackedMu.Lock()
	notified := commandUnackedNotified[deviceID]
	delete(commandUnackedNotified, deviceID)
	commandUnackedMu.Unlock()
	if notified {
		slog.Info("Device acknowledges commands again", "device", deviceID)
		haDismiss(commandUnackedID(deviceID))
	}
}

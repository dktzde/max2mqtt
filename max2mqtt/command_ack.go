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
	// commandAckSeen: devices that acknowledged a command since the start. The notification of an
	// unacknowledged command outlives a restart of the add-on, commandUnackedNotified does not; so
	// the first Ack of each device after the start removes it in any case. Seen 2026-11-18: after the
	// update to 1.8.0 a notification stayed although the next command was acknowledged.
	commandAckSeen = map[string]bool{}
)

// commandAckCallbacks returns what happens after the device acknowledged a command, after the
// last try without an Ack, and when the command was dropped without being sent (duty cycle
// credits, busy CUL, see commandFailure). A SetTemperature telegram (setpoint, mode, boost,
// vacation) also keeps the attribute command_status up to date.
func commandAckCallbacks(deviceID string, typeByte byte, cnt int, description string, tries int) (acked func(), unacked, dropped func(commandFailure)) {
	if typeByte == 0xF1 {
		return func() { commandAcked(deviceID) }, nil, func(commandFailure) { clearWakeUpQueued(deviceID) }
	}
	var seq uint64
	if typeByte == 0x40 {
		seq = setCommandStatus(deviceID, description, statusPending, nil)
		publishAttributes(deviceID)
	}
	acked = func() {
		if typeByte == 0x40 && finishCommandStatus(deviceID, seq, description, statusAcknowledged, nil) {
			publishAttributes(deviceID)
		}
		commandAcked(deviceID)
	}
	failed := func(f commandFailure) {
		switch typeByte {
		case 0x40:
			// back to the state from before the command, unless the device reported one since
			ackTracker.SetpointUnacked(deviceID, cnt)
			if finishCommandStatus(deviceID, seq, description, f.status(), &f) {
				publishAttributes(deviceID)
			}
		case 0x11, 0x12, 0x22, 0x23, 0x82:
			ackTracker.ConfigUnacked(deviceID, cnt, f)
		}
		commandUnacked(deviceID, description, f)
	}
	return acked, failed, failed
}

// commandUnacked tells Home Assistant that a command did not reach the device and why: the device
// did not answer, or the bridge could not send it (duty cycle, CUL)
func commandUnacked(deviceID, description string, f commandFailure) {
	commandUnackedMu.Lock()
	commandUnackedNotified[deviceID] = true
	commandUnackedMu.Unlock()
	haNotify(commandUnackedID(deviceID), f.title(),
		fmt.Sprintf("MAX! device %s, command \"%s\" (%s): %s.\n\n%s Home Assistant shows the last state the device reported. This notification disappears after the next acknowledged command to this device.",
			deviceID, description, time.Now().Format("15:04"), f.summary(), f.hint()))
}

// commandAcked removes the notification of an earlier unacknowledged command to the device
func commandAcked(deviceID string) {
	commandUnackedMu.Lock()
	notified := commandUnackedNotified[deviceID]
	firstAck := !commandAckSeen[deviceID]
	delete(commandUnackedNotified, deviceID)
	commandAckSeen[deviceID] = true
	commandUnackedMu.Unlock()
	if notified {
		slog.Info("Device acknowledges commands again", "device", deviceID)
	}
	if notified || firstAck {
		// first Ack since the start: a notification from before a restart may still be shown
		haDismiss(commandUnackedID(deviceID))
	}
}

package main

import (
	"sync"
	"time"
)

// State of the last SetTemperature command (setpoint, mode, boost, vacation) to a device, published
// in the climate attribute command_status: {"state": "acknowledged", "command": "Set Preset boost",
// "at": "2026-11-17T18:45:15.000+01:00"}. A failed command (state not_acknowledged or not_sent) also
// has reason (see failReason, e.g. "no_ack" or "duty_cycle") and detail (a short English text), so
// an automation can tell a device that did not answer from a command the bridge could not send.
// Not persisted.

// CommandStatus is the state of the last SetTemperature command to a device
type CommandStatus struct {
	State   string `json:"state"`
	Command string `json:"command"`
	At      string `json:"at"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

var (
	commandStatusMu sync.Mutex
	commandStatus   = map[string]CommandStatus{}
)

// setCommandStatus records the state of the last SetTemperature command; f is nil unless it failed
func setCommandStatus(deviceID, command, state string, f *commandFailure) {
	st := CommandStatus{State: state, Command: command, At: time.Now().Format(configStatusTimeLayout)}
	if f != nil {
		st.Reason, st.Detail = string(f.Reason), f.summary()
	}
	commandStatusMu.Lock()
	commandStatus[deviceID] = st
	commandStatusMu.Unlock()
}

// commandStatusOf returns the state of the last SetTemperature command, false when none was sent
func commandStatusOf(deviceID string) (CommandStatus, bool) {
	commandStatusMu.Lock()
	defer commandStatusMu.Unlock()
	st, ok := commandStatus[deviceID]
	return st, ok
}

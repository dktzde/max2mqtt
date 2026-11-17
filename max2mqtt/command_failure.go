package main

import (
	"fmt"
	"time"
)

// Why a command to a device failed. The reason appears in the log, in the Home Assistant
// notification and in the climate attributes config_status and command_status, so a lost command
// can be told apart at a glance: the device did not answer (radio) or the bridge never sent it
// (duty cycle, CUL). Seen 2026-11-17: a boost renewed every four minutes used up the 1 % airtime,
// later commands were dropped for lack of credits, and the notification looked the same as for a
// device that did not answer.

type failReason string

const (
	// failNoAck: the command was sent, the device did not acknowledge it after the last try
	failNoAck failReason = "no_ack"
	// failAckTimeout: a config change got no Ack within the safety timeout (configRollbackAfter)
	failAckTimeout failReason = "ack_timeout"
	// failDutyCycle: not sent (again), the CUL had too few duty cycle credits until command_timeout
	failDutyCycle failReason = "duty_cycle"
	// failCULBusy: not sent (again), the CUL's send queue stayed full or the serial channel was full
	failCULBusy failReason = "cul_busy"
	// failCULNotReady: not sent (again), the CUL did not answer the credit query (reconnect)
	failCULNotReady failReason = "cul_not_ready"
	// failQueueTimeout: not sent (again), the command waited behind other commands until command_timeout
	failQueueTimeout failReason = "queue_timeout"
	// failQueueFull: not sent, the bridge's own command queue was full when the command arrived
	failQueueFull failReason = "queue_full"
)

// statusNotSent is the config_status/command_status state of a command that never went out
const statusNotSent = "not_sent"

// commandFailure describes a command that did not reach the device
type commandFailure struct {
	Reason  failReason
	Tries   int           // how often the telegram was sent
	Credits int           // credits at the last query (duty cycle)
	Needed  int           // credits the command needed
	Waited  time.Duration // how long the command waited before it was dropped
}

// status is the state shown in config_status/command_status: not_acknowledged when the telegram
// went out at least once, not_sent when it never did
func (f commandFailure) status() string {
	if f.Tries > 0 || f.Reason == failNoAck || f.Reason == failAckTimeout {
		return statusNotAcknowledged
	}
	return statusNotSent
}

// notSentText says why the bridge did not send the command (again)
func (f commandFailure) notSentText() string {
	waited := f.Waited.Round(time.Second)
	switch f.Reason {
	case failDutyCycle:
		return fmt.Sprintf("not sent: duty cycle limit (1 %% airtime), credits %d, needed %d, waited %s", f.Credits, f.Needed, waited)
	case failCULBusy:
		return fmt.Sprintf("not sent: the CUL's send queue was busy, waited %s", waited)
	case failCULNotReady:
		return "not sent: the CUL did not answer"
	case failQueueTimeout:
		return fmt.Sprintf("not sent: waited %s behind other commands", waited)
	case failQueueFull:
		return "not sent: the bridge's command queue was full"
	}
	return "not sent"
}

// summary is a short English text for the log, the notification and the attribute detail
func (f commandFailure) summary() string {
	switch f.Reason {
	case failNoAck:
		return fmt.Sprintf("no Ack from the device after %d tries (radio problem, not the duty cycle)", f.Tries)
	case failAckTimeout:
		return "no Ack from the device in time (radio problem, not the duty cycle)"
	}
	if f.Tries > 0 {
		return fmt.Sprintf("sent %d times without an Ack from the device, the next try was %s", f.Tries, f.notSentText())
	}
	return f.notSentText()
}

// title is the title of the Home Assistant notification
func (f commandFailure) title() string {
	switch f.Reason {
	case failNoAck, failAckTimeout:
		return "MAX! command not acknowledged (no answer from the device)"
	case failDutyCycle:
		return "MAX! command not sent (duty cycle limit)"
	case failCULBusy:
		return "MAX! command not sent (CUL busy)"
	case failCULNotReady:
		return "MAX! command not sent (CUL not answering)"
	case failQueueTimeout:
		return "MAX! command not sent (queue timeout)"
	case failQueueFull:
		return "MAX! command not sent (queue full)"
	}
	return "MAX! command failed"
}

// hint tells the user what to do about it
func (f commandFailure) hint() string {
	switch f.Reason {
	case failNoAck, failAckTimeout:
		return "The device did not answer: check its batteries and the distance to the CUL, then send the command again."
	case failDutyCycle:
		return "The CUL may only send 1 % of the time (about 34 telegrams with wakeup per hour); too many commands in a short time (e.g. a boost renewed every few minutes) use it up. Send the command again later."
	case failCULBusy, failCULNotReady:
		return "Check the CUL connection, then send the command again."
	}
	return "Send the command again."
}

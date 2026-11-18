package main

import (
	"strings"
	"testing"
	"time"
)

func resetCommandAckSeen(t *testing.T) {
	t.Helper()
	commandUnackedMu.Lock()
	oldNotified, oldSeen := commandUnackedNotified, commandAckSeen
	commandUnackedNotified, commandAckSeen = map[string]bool{}, map[string]bool{}
	commandUnackedMu.Unlock()
	t.Cleanup(func() {
		commandUnackedMu.Lock()
		commandUnackedNotified, commandAckSeen = oldNotified, oldSeen
		commandUnackedMu.Unlock()
	})
}

// After a restart the bridge does not know which notification it showed: the first Ack of a device
// removes it in any case, later Acks only after a new failure
func TestFirstAckAfterStartDismissesOldNotification(t *testing.T) {
	rec := recordNotifications(t, time.Hour)
	resetCommandAckSeen(t)
	id := "dismiss:" + commandUnackedID(cmdAckDevice)

	commandAcked(cmdAckDevice)
	if strings.Count(rec.list(), id) != 1 {
		t.Fatalf("first Ack after the start did not dismiss: %q", rec.list())
	}
	commandAcked(cmdAckDevice)
	if strings.Count(rec.list(), id) != 1 {
		t.Fatalf("second Ack dismissed again: %q", rec.list())
	}
	commandUnacked(cmdAckDevice, "Set Mode auto", commandFailure{Reason: failNoAck, Tries: 3})
	commandAcked(cmdAckDevice)
	if strings.Count(rec.list(), id) != 2 {
		t.Fatalf("Ack after a new failure did not dismiss: %q", rec.list())
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func commandStatusAttr(t *testing.T, fc *fakeMQTT, deviceID string) CommandStatus {
	t.Helper()
	var attrs struct {
		CommandStatus CommandStatus `json:"command_status"`
	}
	raw := fc.get("max/" + deviceID + "/attributes")
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		t.Fatalf("attributes %q: %v", raw, err)
	}
	return attrs.CommandStatus
}

func TestFailureStatusAndTexts(t *testing.T) {
	cases := []struct {
		f      commandFailure
		status string
		title  string
		text   string
	}{
		{commandFailure{Reason: failNoAck, Tries: 3}, statusNotAcknowledged, "no answer from the device", "no Ack from the device after 3 tries"},
		{commandFailure{Reason: failAckTimeout}, statusNotAcknowledged, "no answer from the device", "not the duty cycle"},
		{commandFailure{Reason: failDutyCycle, Credits: 133, Needed: 200, Waited: time.Minute}, statusNotSent, "duty cycle limit", "credits 133, needed 200, waited 1m0s"},
		{commandFailure{Reason: failDutyCycle, Tries: 1, Credits: 90, Needed: 200}, statusNotAcknowledged, "duty cycle limit", "sent 1 times without an Ack from the device, the next try was not sent: duty cycle"},
		{commandFailure{Reason: failCULBusy}, statusNotSent, "CUL busy", "send queue was busy"},
		{commandFailure{Reason: failCULNotReady}, statusNotSent, "CUL not answering", "did not answer"},
		{commandFailure{Reason: failQueueTimeout}, statusNotSent, "queue timeout", "behind other commands"},
	}
	for _, c := range cases {
		if got := c.f.status(); got != c.status {
			t.Errorf("%s/%d: status %q, want %q", c.f.Reason, c.f.Tries, got, c.status)
		}
		if !strings.Contains(c.f.title(), c.title) {
			t.Errorf("%s: title %q, want %q in it", c.f.Reason, c.f.title(), c.title)
		}
		if !strings.Contains(c.f.summary(), c.text) {
			t.Errorf("%s: summary %q, want %q in it", c.f.Reason, c.f.summary(), c.text)
		}
	}
}

// A boost the device does not acknowledge: the notification and command_status name the device,
// not the duty cycle
func TestUnackedBoostSaysNoAnswerFromDevice(t *testing.T) {
	fc, sent, rec := startCommandTest(t, 50*time.Millisecond)
	stateMutex.Lock()
	stateCache[cmdAckDevice] = &MaxDeviceData{Temperature: 13, Mode: "auto", HVACMode: "auto", PresetMode: "none"}
	stateMutex.Unlock()

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/preset/set", payload: "boost"})
	if cs := commandStatusAttr(t, fc, cmdAckDevice); cs.State != statusPending || cs.Command != "Set Preset boost" {
		t.Fatalf("command_status %+v right after sending, want pending", cs)
	}
	for i := 0; i < 3; i++ {
		receive(t, sent)
	}
	waitForNotification(t, rec, "MAX! command not acknowledged (no answer from the device)")
	cs := commandStatusAttr(t, fc, cmdAckDevice)
	if cs.State != statusNotAcknowledged || cs.Reason != string(failNoAck) || !strings.Contains(cs.Detail, "after 3 tries") {
		t.Fatalf("command_status %+v, want not_acknowledged / no_ack", cs)
	}
}

// An acknowledged boost shows command_status acknowledged without reason
func TestAckedBoostCommandStatus(t *testing.T) {
	fc, sent, _ := startCommandTest(t, time.Second)
	stateMutex.Lock()
	stateCache[cmdAckDevice] = &MaxDeviceData{Temperature: 13, Mode: "auto", HVACMode: "auto", PresetMode: "none"}
	stateMutex.Unlock()

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/preset/set", payload: "boost"})
	cnt := cmdCounter(t, receive(t, sent))
	txMgr.AckReceived(cmdAckDevice, cnt)
	deadline := time.Now().Add(time.Second)
	for {
		cs := commandStatusAttr(t, fc, cmdAckDevice)
		if cs.State == statusAcknowledged && cs.Reason == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command_status %+v after the Ack, want acknowledged", cs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A late result of an older command must not overwrite the status of a newer, pending one
func TestOlderCommandResultDoesNotOverwriteNewerStatus(t *testing.T) {
	resetTestState()
	seqA := setCommandStatus(cmdAckDevice, "Set Preset boost", statusPending, nil)
	seqB := setCommandStatus(cmdAckDevice, "Set Preset boost", statusPending, nil)
	if finishCommandStatus(cmdAckDevice, seqA, "Set Preset boost", statusAcknowledged, nil) {
		t.Fatal("the older command updated the status")
	}
	if st, _ := commandStatusOf(cmdAckDevice); st.State != statusPending {
		t.Fatalf("newer command should still be pending: %+v", st)
	}
	f := commandFailure{Reason: failNoAck, Tries: 3}
	if !finishCommandStatus(cmdAckDevice, seqB, "Set Preset boost", f.status(), &f) {
		t.Fatal("the latest command did not update the status")
	}
	if st, _ := commandStatusOf(cmdAckDevice); st.State != statusNotAcknowledged || st.Reason != string(failNoAck) {
		t.Fatalf("unexpected status: %+v", st)
	}
}

// A command rejected by a full queue ends with not_sent instead of staying pending
func TestQueueFullCallsOnDropped(t *testing.T) {
	resetTestState()
	tm := &TransmissionManager{queue: make(chan QueuedCommand)} // no reader: always full
	var got *commandFailure
	tm.enqueue(QueuedCommand{DeviceID: cmdAckDevice, CreatedAt: time.Now(), OnDropped: func(f commandFailure) { got = &f }})
	if got == nil || got.Reason != failQueueFull || got.status() != statusNotSent {
		t.Fatalf("expected queue_full/not_sent, got %+v", got)
	}
}

// A credit shortage resolved by a successful send does not explain a later timeout
func TestTimeoutAfterSentTryIsNoAckNotDutyCycle(t *testing.T) {
	resetTestState()
	cmd := &QueuedCommand{DeviceID: cmdAckDevice, Tries: 1, CreatedAt: time.Now()}
	tm := &TransmissionManager{}
	tm.dropAfterTimeout(cmd)
	if f := cmd.failure(); f.Reason != failNoAck || f.status() != statusNotAcknowledged {
		t.Fatalf("expected no_ack, got %+v", f)
	}
}

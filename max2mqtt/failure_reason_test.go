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

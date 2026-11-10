package main

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const cmdAckDevice = "0A1B2C"

// cmdCounter returns the message counter of a CUL send command "Zs<len><cnt>..."
func cmdCounter(t *testing.T, cmd string) int {
	t.Helper()
	if len(cmd) < 6 {
		t.Fatalf("short command %q", cmd)
	}
	n, err := strconv.ParseUint(cmd[4:6], 16, 8)
	if err != nil {
		t.Fatalf("counter of %q: %v", cmd, err)
	}
	return int(n)
}

func receive(t *testing.T, sent <-chan string) string {
	t.Helper()
	select {
	case c := <-sent:
		return c
	case <-time.After(time.Second):
		t.Fatal("nothing sent")
	}
	return ""
}

// startCommandTest starts the dispatcher with a short Ack timeout, an Ack tracker, recorded
// Home Assistant notifications and a device that was heard just now (no WakeUp)
func startCommandTest(t *testing.T, ackTimeout time.Duration) (*fakeMQTT, <-chan string, *notifyRecorder) {
	t.Helper()
	setBulkAck(t, ackTimeout, 3)
	rec := recordNotifications(t, time.Hour)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	resetWakeUpState(t)
	initAckTracker()
	commandUnackedMu.Lock()
	commandUnackedNotified = map[string]bool{}
	commandUnackedMu.Unlock()
	touchLastSeen(cmdAckDevice)
	start()
	return mqttClient.(*fakeMQTT), sent, rec
}

func TestCommandWaitsForAckBeforeNextTelegram(t *testing.T) {
	setBulkAck(t, 2*time.Second, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	txMgr.EnqueueAcked(cmdAckDevice, "ZsCONFIG", "config valve", 11, 3, nil, nil)
	txMgr.Enqueue(cmdAckDevice, "ZsBOOST", "boost")
	start()

	expectSent(t, sent, "ZsCONFIG")
	// The device is still answering the config: the boost must not go out yet
	expectNothingSent(t, sent, 150*time.Millisecond)
	txMgr.AckReceived(cmdAckDevice, 10)
	txMgr.AckReceived("1D2E3F", 11)
	expectNothingSent(t, sent, 100*time.Millisecond)

	txMgr.AckReceived(cmdAckDevice, 11)
	expectSent(t, sent, "ZsBOOST")
}

func TestCommandSentAgainWithoutAck(t *testing.T) {
	setBulkAck(t, 60*time.Millisecond, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	var acked, unacked atomic.Int32
	txMgr.EnqueueAcked(cmdAckDevice, "ZsBOOST", "boost", 21, 3, func() { acked.Add(1) }, func() { unacked.Add(1) })
	txMgr.Enqueue(cmdAckDevice, "ZsNEXT", "next")
	start()

	for i := 0; i < 3; i++ {
		expectSent(t, sent, "ZsBOOST")
	}
	expectSent(t, sent, "ZsNEXT")
	if acked.Load() != 0 || unacked.Load() != 1 {
		t.Fatalf("onAcked %d, onUnacked %d times, want 0 and 1", acked.Load(), unacked.Load())
	}
}

func TestCommandRetryStopsAfterAck(t *testing.T) {
	setBulkAck(t, 100*time.Millisecond, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	var acked, unacked atomic.Int32
	txMgr.EnqueueAcked(cmdAckDevice, "ZsBOOST", "boost", 31, 3, func() { acked.Add(1) }, func() { unacked.Add(1) })
	start()

	expectSent(t, sent, "ZsBOOST")
	expectSent(t, sent, "ZsBOOST") // first try lost
	txMgr.AckReceived(cmdAckDevice, 31)
	expectNothingSent(t, sent, 250*time.Millisecond)
	if acked.Load() != 1 || unacked.Load() != 0 {
		t.Fatalf("onAcked %d, onUnacked %d times, want 1 and 0", acked.Load(), unacked.Load())
	}
}

func TestWakeUpIsNotSentAgain(t *testing.T) {
	setBulkAck(t, 60*time.Millisecond, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	txMgr.EnqueueAcked(cmdAckDevice, "ZsWAKE", "WakeUp", 41, 1, nil, nil)
	txMgr.Enqueue(cmdAckDevice, "ZsNEXT", "next")
	start()

	expectSent(t, sent, "ZsWAKE")
	expectSent(t, sent, "ZsNEXT")
}

func TestOnlyDeviceCommandsWaitForAck(t *testing.T) {
	cases := []struct {
		dst  string
		typ  byte
		want int
	}{
		{cmdAckDevice, 0x40, 3}, {cmdAckDevice, 0x11, 3}, {cmdAckDevice, 0x12, 3},
		{cmdAckDevice, 0x20, 3}, {cmdAckDevice, 0x21, 3}, {cmdAckDevice, 0x22, 3}, {cmdAckDevice, 0x23, 3},
		{cmdAckDevice, 0x82, 3}, {cmdAckDevice, 0xF1, 1},
		{cmdAckDevice, 0x03, 0}, {cmdAckDevice, 0xF0, 0}, {"000000", 0x40, 0}, {"000000", 0x03, 0},
	}
	for _, c := range cases {
		if got := commandAckTries(c.dst, c.typ); got != c.want {
			t.Errorf("commandAckTries(%s, 0x%02X) = %d, want %d", c.dst, c.typ, got, c.want)
		}
	}
}

// Seen 2026-11-10: the boost was not acknowledged and not sent again
func TestUnacknowledgedBoostIsSentAgainThenStateRestored(t *testing.T) {
	fc, sent, rec := startCommandTest(t, 60*time.Millisecond)
	stateMutex.Lock()
	stateCache[cmdAckDevice] = &MaxDeviceData{Temperature: 13, Mode: "auto", HVACMode: "auto", PresetMode: "none"}
	stateMutex.Unlock()

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/preset/set", payload: "boost"})
	if st := fc.get("max/" + cmdAckDevice + "/state"); !strings.Contains(st, `"preset_mode":"boost"`) {
		t.Fatalf("optimistic boost not shown: %s", st)
	}
	first := receive(t, sent)
	for i := 0; i < 2; i++ {
		if again := receive(t, sent); again != first {
			t.Fatalf("retry %d sent %s, want %s", i+1, again, first)
		}
	}
	waitForNotification(t, rec, "notify:"+commandUnackedID(cmdAckDevice))
	st := fc.get("max/" + cmdAckDevice + "/state")
	if !strings.Contains(st, `"mode":"auto"`) || !strings.Contains(st, `"preset_mode":"none"`) || !strings.Contains(st, `"hvac_mode":"auto"`) {
		t.Fatalf("state not restored after the boost was not acknowledged: %s", st)
	}
}

func TestUnacknowledgedSetpointKeepsStateReportedMeanwhile(t *testing.T) {
	fc, sent, _ := startCommandTest(t, 80*time.Millisecond)
	stateMutex.Lock()
	stateCache[cmdAckDevice] = &MaxDeviceData{Temperature: 13, Mode: "auto", HVACMode: "auto", PresetMode: "none"}
	stateMutex.Unlock()

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/preset/set", payload: "boost"})
	receive(t, sent)
	// The device reports manual 20 °C (changed on the device) while the boost waits for its Ack
	updateDeviceState(cmdAckDevice, &MaxDeviceData{Temperature: 20, Mode: "manual", HVACMode: "heat"})
	receive(t, sent)
	receive(t, sent)
	time.Sleep(150 * time.Millisecond)
	st := fc.get("max/" + cmdAckDevice + "/state")
	if !strings.Contains(st, `"mode":"manual"`) || !strings.Contains(st, `"temperature":20`) {
		t.Fatalf("the state reported by the device was replaced: %s", st)
	}
}

func TestBoostAcknowledgedOnRetryDismissesNotification(t *testing.T) {
	fc, sent, rec := startCommandTest(t, 100*time.Millisecond)
	stateMutex.Lock()
	stateCache[cmdAckDevice] = &MaxDeviceData{Temperature: 13, Mode: "auto", HVACMode: "auto", PresetMode: "none"}
	stateMutex.Unlock()
	commandUnacked(cmdAckDevice, "Set Preset boost", 3) // an earlier command got lost

	handleMQTTPresetCommand(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/preset/set", payload: "boost"})
	receive(t, sent)
	cnt := cmdCounter(t, receive(t, sent)) // second try, same telegram
	txMgr.AckReceived(cmdAckDevice, cnt)
	if !ackTracker.ApplySetpointAck(cmdAckDevice, cnt) {
		t.Fatal("Ack of the retry did not apply the boost")
	}
	expectNothingSent(t, sent, 250*time.Millisecond)
	if st := fc.get("max/" + cmdAckDevice + "/state"); !strings.Contains(st, `"preset_mode":"boost"`) {
		t.Fatalf("boost not shown after the Ack: %s", st)
	}
	waitForNotification(t, rec, "dismiss:"+commandUnackedID(cmdAckDevice))
}

func TestUnacknowledgedConfigRestoresPreviousValuesAtOnce(t *testing.T) {
	_, sent, rec := startCommandTest(t, 60*time.Millisecond)
	importKnownConfig(t, cmdAckDevice)

	handleMQTTBoostDuration(nil, fakeMessage{topic: "max/" + cmdAckDevice + "/boost_duration/set", payload: "30"})
	if d := boostDurationOf(cmdAckDevice); d != 30 {
		t.Fatalf("optimistic boost duration %d, want 30", d)
	}
	for i := 0; i < 3; i++ {
		receive(t, sent)
	}
	deadline := time.Now().Add(time.Second)
	for boostDurationOf(cmdAckDevice) != 5 {
		if time.Now().After(deadline) {
			t.Fatalf("boost duration %d after the last try, want the previous 5", boostDurationOf(cmdAckDevice))
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForNotification(t, rec, "notify:"+commandUnackedID(cmdAckDevice))
}

func waitForNotification(t *testing.T, rec *notifyRecorder, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.list(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s, got %q", want, rec.list())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func boostDurationOf(deviceID string) int {
	v := getValveConfig(deviceID)
	valveMutex.RLock()
	defer valveMutex.RUnlock()
	return v.BoostDuration
}

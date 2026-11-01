package main

import (
	"sync/atomic"
	"testing"
	"time"
)

const ackWaitTestDevice = "0A1B2C"

// setBulkAck sets the Ack timeout and tries for one test. Call it before startTxTest, so the
// values are restored only after the dispatcher has stopped.
func setBulkAck(t *testing.T, timeout time.Duration, tries int) {
	t.Helper()
	oldTimeout, oldTries := bulkAckTimeout, bulkMaxTries
	bulkAckTimeout, bulkMaxTries = timeout, tries
	t.Cleanup(func() { bulkAckTimeout, bulkMaxTries = oldTimeout, oldTries })
}

func expectSent(t *testing.T, sent <-chan string, want string) {
	t.Helper()
	select {
	case c := <-sent:
		if c != want {
			t.Fatalf("sent %s, want %s", c, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s not sent", want)
	}
}

func expectNothingSent(t *testing.T, sent <-chan string, wait time.Duration) {
	t.Helper()
	select {
	case c := <-sent:
		t.Fatalf("unexpected TX %s", c)
	case <-time.After(wait):
	}
}

func TestWeekProfileWaitsForAckBeforeNextDay(t *testing.T) {
	setBulkAck(t, 2*time.Second, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	txMgr.EnqueueBulkAcked(ackWaitTestDevice, "ZsMON", "week profile Mon", 11, nil)
	txMgr.EnqueueBulkAcked(ackWaitTestDevice, "ZsTUE", "week profile Tue", 12, nil)
	start()

	expectSent(t, sent, "ZsMON")
	// The device is still answering: the next day must not go out yet
	expectNothingSent(t, sent, 150*time.Millisecond)
	// Acks for another telegram or from another device do not count
	txMgr.AckReceived(ackWaitTestDevice, 10)
	txMgr.AckReceived("1D2E3F", 11)
	expectNothingSent(t, sent, 100*time.Millisecond)

	txMgr.AckReceived(ackWaitTestDevice, 11)
	expectSent(t, sent, "ZsTUE")
}

func TestWeekProfileSentAgainWithoutAck(t *testing.T) {
	setBulkAck(t, 60*time.Millisecond, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	var unacked atomic.Int32
	txMgr.EnqueueBulkAcked(ackWaitTestDevice, "ZsMON", "week profile Mon", 21, func() { unacked.Add(1) })
	txMgr.EnqueueBulkAcked(ackWaitTestDevice, "ZsTUE", "week profile Tue", 22, nil)
	start()

	for i := 0; i < 3; i++ {
		expectSent(t, sent, "ZsMON")
	}
	// After the last try the bridge gives up on Monday and goes on with Tuesday
	expectSent(t, sent, "ZsTUE")
	if got := unacked.Load(); got != 1 {
		t.Fatalf("onUnacked called %d times, want 1", got)
	}
}

func TestWeekProfileRetryStopsAfterAck(t *testing.T) {
	setBulkAck(t, 100*time.Millisecond, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	var unacked atomic.Int32
	txMgr.EnqueueBulkAcked(ackWaitTestDevice, "ZsMON", "week profile Mon", 31, func() { unacked.Add(1) })
	start()

	expectSent(t, sent, "ZsMON")
	expectSent(t, sent, "ZsMON") // first try lost
	txMgr.AckReceived(ackWaitTestDevice, 31)
	expectNothingSent(t, sent, 250*time.Millisecond)
	if got := unacked.Load(); got != 0 {
		t.Fatalf("onUnacked called %d times after the Ack", got)
	}
}

func TestBulkWithoutCounterDoesNotWait(t *testing.T) {
	setBulkAck(t, time.Minute, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(900)
	txMgr.EnqueueBulk(ackWaitTestDevice, "ZsMON", "week profile Mon")
	txMgr.EnqueueBulk(ackWaitTestDevice, "ZsTUE", "week profile Tue")
	start()
	expectSent(t, sent, "ZsMON")
	expectSent(t, sent, "ZsTUE")
}

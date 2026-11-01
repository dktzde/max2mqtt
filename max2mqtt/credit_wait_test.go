package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startTxTest starts a TX manager with a fake CUL that answers every credit query "X"
// with the returned credits and passes all other commands to the returned channel.
// The dispatcher only runs once start() is called; both goroutines end with the test.
func startTxTest(t *testing.T, timeout time.Duration) (*atomic.Int32, <-chan string, func()) {
	resetTestState()
	creditRetryInterval = 20 * time.Millisecond
	ser := serialWrite
	stop := make(chan struct{})
	mgr := &TransmissionManager{
		MinCredits:     200,
		MaxQueue:       5,
		Timeout:        timeout,
		queue:          make(chan QueuedCommand, 100),
		bulkQueue:      make(chan QueuedCommand, 200),
		creditResponse: make(chan struct{}, 1),
		stop:           stop,
	}
	txMgr = mgr
	credits := &atomic.Int32{}
	sent := make(chan string, 10)
	var wg sync.WaitGroup
	culStop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case cmd := <-ser:
				if cmd == "X" {
					mgr.UpdateCredits(int(credits.Load()), 0)
					mgr.SignalCreditResponse()
				} else {
					sent <- cmd
				}
			case <-culStop:
				return
			}
		}
	}()
	var dwg sync.WaitGroup
	start := func() {
		dwg.Add(1)
		go func() {
			defer dwg.Done()
			mgr.dispatcherLoop()
		}()
	}
	t.Cleanup(func() {
		close(stop)
		dwg.Wait()
		close(culStop)
		wg.Wait()
	})
	return credits, sent, start
}

func TestCommandWaitsForCredits(t *testing.T) {
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(100)
	start()
	txMgr.Enqueue("0A1B2C", "Zs01", "setpoint")
	select {
	case c := <-sent:
		t.Fatalf("sent without credits: %s", c)
	case <-time.After(100 * time.Millisecond):
	}
	credits.Store(500)
	select {
	case c := <-sent:
		if c != "Zs01" {
			t.Fatalf("unexpected TX %s", c)
		}
	case <-time.After(time.Second):
		t.Fatal("command dropped instead of sent when credits came back")
	}
}

func TestNormalCommandOvertakesWaitingBulk(t *testing.T) {
	credits, sent, start := startTxTest(t, time.Minute)
	credits.Store(100)
	txMgr.EnqueueBulk("0A1B2C", "ZsBULK", "week profile")
	start()
	time.Sleep(60 * time.Millisecond)
	txMgr.Enqueue("0A1B2C", "ZsNORMAL", "setpoint")
	time.Sleep(30 * time.Millisecond)
	credits.Store(900)
	var order []string
	for len(order) < 2 {
		select {
		case c := <-sent:
			order = append(order, c)
		case <-time.After(time.Second):
			t.Fatalf("missing TX, got %v", order)
		}
	}
	if strings.Join(order, ",") != "ZsNORMAL,ZsBULK" {
		t.Fatalf("order %v", order)
	}
}

func TestCommandDroppedAfterTimeout(t *testing.T) {
	credits, sent, start := startTxTest(t, 80*time.Millisecond)
	start()
	txMgr.Enqueue("0A1B2C", "Zs01", "setpoint")
	time.Sleep(200 * time.Millisecond)
	credits.Store(900)
	select {
	case c := <-sent:
		t.Fatalf("expired command was sent: %s", c)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBulkKeepsCreditReserve(t *testing.T) {
	credits, sent, start := startTxTest(t, time.Minute)
	txMgr.ReservePercent = 20
	if got := txMgr.minCredits(QueuedCommand{Bulk: true}); got != 380 {
		t.Fatalf("bulk min credits %d, want 380", got)
	}
	credits.Store(300)
	start()
	txMgr.EnqueueBulk("0A1B2C", "ZsBULK", "week profile")
	select {
	case c := <-sent:
		t.Fatalf("bulk sent inside the reserve: %s", c)
	case <-time.After(100 * time.Millisecond):
	}
	txMgr.Enqueue("0A1B2C", "ZsNORMAL", "setpoint")
	select {
	case c := <-sent:
		if c != "ZsNORMAL" {
			t.Fatalf("unexpected TX %s", c)
		}
	case <-time.After(time.Second):
		t.Fatal("direct command did not use the reserve")
	}
	credits.Store(400)
	select {
	case c := <-sent:
		if c != "ZsBULK" {
			t.Fatalf("unexpected TX %s", c)
		}
	case <-time.After(time.Second):
		t.Fatal("bulk not sent above the reserve")
	}
}

func TestBulkNeverDropped(t *testing.T) {
	credits, sent, start := startTxTest(t, 50*time.Millisecond)
	start()
	txMgr.enqueue(QueuedCommand{Payload: "ZsBULK", DeviceID: "0A1B2C", Description: "week profile", CreatedAt: time.Now().Add(-24 * time.Hour), Bulk: true})
	time.Sleep(150 * time.Millisecond)
	credits.Store(900)
	select {
	case c := <-sent:
		if c != "ZsBULK" {
			t.Fatalf("unexpected TX %s", c)
		}
	case <-time.After(time.Second):
		t.Fatal("old bulk command was dropped")
	}
}

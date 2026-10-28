package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type notifyRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *notifyRecorder) add(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *notifyRecorder) list() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.events, ",")
}

// recordNotifications replaces the Home Assistant calls and shortens the bulk times for one test.
// Call it before startTxTest, so the values are restored after the dispatcher ended.
func recordNotifications(t *testing.T, notifyAfter, timeout time.Duration) *notifyRecorder {
	rec := &notifyRecorder{}
	oldNotify, oldDismiss, oldAfter, oldTimeout := haNotify, haDismiss, bulkNotifyAfter, bulkCommandTimeout
	haNotify = func(id, title, message string) { rec.add("notify:" + id + ":" + title) }
	haDismiss = func(id string) { rec.add("dismiss:" + id) }
	bulkNotifyAfter, bulkCommandTimeout = notifyAfter, timeout
	t.Cleanup(func() {
		haNotify, haDismiss, bulkNotifyAfter, bulkCommandTimeout = oldNotify, oldDismiss, oldAfter, oldTimeout
	})
	return rec
}

func TestBulkWaitNotifiesThenDismissesWhenSent(t *testing.T) {
	rec := recordNotifications(t, 50*time.Millisecond, time.Minute)
	credits, sent, start := startTxTest(t, time.Minute)
	txMgr.bulkWait = newBulkWaitTracker()
	credits.Store(100)
	txMgr.EnqueueBulk("0A1B2C", "ZsMON", "week profile Mon")
	txMgr.EnqueueBulk("0A1B2C", "ZsTUE", "week profile Tue")
	start()
	time.Sleep(150 * time.Millisecond)
	if got := rec.list(); got != "notify:max2mqtt_week_profile_0A1B2C:MAX! week profile waiting" {
		t.Fatalf("expected one waiting notification, got %q", got)
	}
	credits.Store(900)
	for i := 0; i < 2; i++ {
		select {
		case <-sent:
		case <-time.After(time.Second):
			t.Fatal("week profile not sent after credits came back")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := rec.list(); !strings.HasSuffix(got, ",dismiss:max2mqtt_week_profile_0A1B2C") || strings.Count(got, "notify:") != 1 {
		t.Fatalf("expected dismiss after the last message, got %q", got)
	}
}

func TestBulkDiscardedAfterTimeoutNotifies(t *testing.T) {
	rec := recordNotifications(t, 30*time.Millisecond, 120*time.Millisecond)
	_, sent, start := startTxTest(t, time.Minute)
	txMgr.bulkWait = newBulkWaitTracker()
	txMgr.EnqueueBulk("0A1B2C", "ZsMON", "week profile Mon")
	start()
	time.Sleep(250 * time.Millisecond)
	select {
	case c := <-sent:
		t.Fatalf("expired week profile was sent: %s", c)
	default:
	}
	got := rec.list()
	if !strings.HasPrefix(got, "notify:max2mqtt_week_profile_0A1B2C:MAX! week profile waiting") ||
		!strings.HasSuffix(got, "notify:max2mqtt_week_profile_0A1B2C:MAX! week profile discarded") ||
		strings.Contains(got, "dismiss") {
		t.Fatalf("expected waiting then discarded notification, got %q", got)
	}
}

func TestBulkSentQuicklyNoNotification(t *testing.T) {
	rec := recordNotifications(t, time.Minute, 2*time.Minute)
	credits, sent, start := startTxTest(t, time.Minute)
	txMgr.bulkWait = newBulkWaitTracker()
	credits.Store(900)
	start()
	txMgr.EnqueueBulk("0A1B2C", "ZsMON", "week profile Mon")
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("week profile not sent")
	}
	time.Sleep(20 * time.Millisecond)
	if got := rec.list(); got != "" {
		t.Fatalf("unexpected notification %q", got)
	}
}

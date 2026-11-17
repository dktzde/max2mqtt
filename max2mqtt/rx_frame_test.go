package main

import (
	"strings"
	"testing"
	"time"
)

// Lines from the device log of 2026-11-17 (addresses replaced)
const (
	goodAck    = "Z0EF102020A1B2C01537300015B1E27"
	goodStatus = "Z0F0004601A2B3C0000000018212600BF"
	// several telegrams glued into one line: length byte 0x1D, 29 bytes follow (fits, but no MAX!
	// telegram is that long)
	gluedLine = "Z1D830E3902020A1B2C01537300015852282C840E3A02020A1B2C01537300"
	// the longest real telegram: week profile part with 7 points (length 0x19)
	weekProfilePart = "Z19010010015373" + "0A1B2C" + "00" + "08" + "3C443CE43520452045204520" + "4520"
)

func newTestRXCheck(t *testing.T) (*rxFrameCheck, *notifyRecorder) {
	t.Helper()
	rec := &notifyRecorder{}
	oldNotify, oldDismiss := haNotify, haDismiss
	haNotify = func(id, title, message string) { rec.add("notify:" + id + ":" + title) }
	haDismiss = func(id string) { rec.add("dismiss:" + id) }
	t.Cleanup(func() { haNotify, haDismiss = oldNotify, oldDismiss })
	return &rxFrameCheck{now: time.Now, reconnect: func(reason string) { rec.add("reconnect:" + reason) }}, rec
}

func TestGoodLinesPass(t *testing.T) {
	c, _ := newTestRXCheck(t)
	for _, l := range []string{goodAck, goodStatus, goodAck + "C4", weekProfilePart} {
		if !c.accept(l) {
			t.Fatalf("good line %s dropped", l)
		}
	}
	if c.accept(goodAck + "C4C4") {
		t.Fatal("line with two bytes too many accepted")
	}
}

func TestGluedLineIsDropped(t *testing.T) {
	c, _ := newTestRXCheck(t)
	if c.accept(gluedLine) {
		t.Fatal("glued line accepted")
	}
	// short fragments and non-telegram lines pass to the parser, which drops or handles them
	for _, l := range []string{"Z00", "Z015B", "00  401", "V 1.26.08 a-culfw"} {
		if !c.accept(l) {
			t.Fatalf("%q dropped by the length check", l)
		}
	}
}

func TestManyGarbledLinesNotifyAndReconnect(t *testing.T) {
	c, rec := newTestRXCheck(t)
	for i := 0; i < garbledLimit; i++ {
		c.accept(gluedLine)
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.list(), "notify:"+garbledNotifyID) || !strings.Contains(rec.list(), "reconnect:garbled lines") {
		if time.Now().After(deadline) {
			t.Fatalf("no notification and reconnect after %d garbled lines: %q", garbledLimit, rec.list())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// quiet for garbledQuiet: the next good line removes the notification
	c.mu.Lock()
	c.lastBad = time.Now().Add(-garbledQuiet)
	c.mu.Unlock()
	c.accept(goodAck)
	deadline = time.Now().Add(time.Second)
	for !strings.Contains(rec.list(), "dismiss:"+garbledNotifyID) {
		if time.Now().After(deadline) {
			t.Fatalf("notification not dismissed: %q", rec.list())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

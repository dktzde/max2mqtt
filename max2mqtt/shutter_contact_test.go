package main

import (
	"testing"
	"time"
)

func TestDecodeShutterContact(t *testing.T) {
	cases := []struct {
		payload byte
		window  string
		battery string
		rf      bool
	}{
		{0x10, "closed", "ok", false},
		{0x12, "open", "ok", false},
		{0x92, "open", "low", false},
		{0x50, "closed", "ok", true},
	}
	for _, c := range cases {
		d := decodeShutterContact([]byte{c.payload})
		if d.Window != c.window || d.Battery != c.battery || d.RFError != c.rf {
			t.Errorf("0x%02X: got %+v, want window=%s battery=%s rf=%v", c.payload, *d, c.window, c.battery, c.rf)
		}
	}
	if decodeShutterContact(nil) != nil {
		t.Error("empty payload should return nil")
	}
}

func TestShutterRepeat(t *testing.T) {
	now := time.Now()
	if isShutterRepeat("AAAAAA", 5, now) {
		t.Error("first message is no repeat")
	}
	if !isShutterRepeat("AAAAAA", 5, now.Add(time.Second)) {
		t.Error("same counter within window is a repeat")
	}
	if isShutterRepeat("AAAAAA", 6, now.Add(2*time.Second)) {
		t.Error("new counter is no repeat")
	}
	if isShutterRepeat("AAAAAA", 6, now.Add(time.Minute)) {
		t.Error("same counter after the window is no repeat")
	}
}

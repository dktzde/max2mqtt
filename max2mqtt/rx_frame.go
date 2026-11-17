package main

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Garbled receive lines. Seen 2026-11-17 on a MAX! Cube with a-culfw: after a burst of telegrams
// (a thermostat answering the bridge and forwarding to two link partners at the same time) the
// CUL delivered several telegrams glued into one line with a wrong length byte, or split over two
// lines, for eight minutes until it was power-cycled. The bridge read Acks it could not match and
// created devices from fragments of real addresses.
//
// Every garbled line of that day had the length byte 0x1D (29) and exactly 29 bytes after it, so
// the length matched; but no MAX! telegram is that long: the longest is a week profile part with
// seven points (0x19 = 25 bytes, see week_profile.go), a PairPing has 0x17. A line is dropped when
// its length byte is above maxTelegramLength or does not fit the bytes that follow (one extra
// byte is allowed: culfw appends the RSSI in report mode X21/X25).

const (
	// maxTelegramLength is the largest length byte of a MAX! telegram (week profile part, 7 points)
	maxTelegramLength = 0x19
	// garbledLimit garbled lines within garbledWindow → notification and reconnect
	garbledLimit  = 5
	garbledWindow = 2 * time.Minute
	// garbledQuiet: the notification goes away after this long without a garbled line
	garbledQuiet = 10 * time.Minute
)

// garbledNotifyID is the Home Assistant notification for a CUL that delivers garbled lines
const garbledNotifyID = "max2mqtt_cul_garbled"

type rxFrameCheck struct {
	mu       sync.Mutex
	garbled  []time.Time
	lastBad  time.Time
	notified bool
	now      func() time.Time
	// reconnect connects the CUL again after many garbled lines (tests record it instead)
	reconnect func(reason string)
}

var rxCheck = &rxFrameCheck{now: time.Now, reconnect: func(reason string) { cul.reconnect(reason) }}

// reset forgets the garbled lines (new CUL connection)
func (c *rxFrameCheck) reset() {
	c.mu.Lock()
	c.garbled = nil
	c.mu.Unlock()
}

// accept reports whether a "Z" line can be a MAX! telegram: its length byte is at most
// maxTelegramLength and fits the bytes that follow (plus an optional RSSI byte). Lines that are no
// hex telegram (credit reports, version) or too short for a header pass to the parser unchecked.
func (c *rxFrameCheck) accept(raw string) bool {
	if len(raw) < 3 || raw[0] != 'Z' {
		return true
	}
	data, err := hex.DecodeString(raw[1:])
	if err != nil || len(data) < 12 {
		return true
	}
	length, got := int(data[0]), len(data)-1
	c.mu.Lock()
	defer c.mu.Unlock()
	if length <= maxTelegramLength && (got == length || got == length+1) {
		c.maybeDismiss()
		return true
	}
	c.garbledLine(raw, length, got)
	return false
}

// garbledLine drops a line and raises the alarm when they pile up; the caller holds c.mu
func (c *rxFrameCheck) garbledLine(raw string, length, got int) {
	now := c.now()
	c.lastBad = now
	slog.Warn("Garbled CUL line dropped: no MAX! telegram has this length", "length_byte", length, "bytes", got, "max", maxTelegramLength, "raw", raw)
	keep := c.garbled[:0]
	for _, t := range c.garbled {
		if now.Sub(t) < garbledWindow {
			keep = append(keep, t)
		}
	}
	c.garbled = append(keep, now)
	if len(c.garbled) < garbledLimit {
		return
	}
	c.garbled = nil
	slog.Error("The CUL delivers garbled lines; reconnecting", "lines", garbledLimit, "within", garbledWindow)
	c.notified = true
	go haNotify(garbledNotifyID, "MAX! CUL receives garbled data",
		fmt.Sprintf("Since %s the CUL delivers MAX! messages whose length does not fit (several telegrams in one line, or one split over two). The bridge drops them, so Acks get lost and commands look unacknowledged. It reconnects to the CUL now; if this notification comes back, unplug the CUL (power) for a few seconds. It disappears after %d minutes without a garbled line.",
			now.Format("15:04"), int(garbledQuiet.Minutes())))
	go c.reconnect("garbled lines")
}

// maybeDismiss removes the notification after garbledQuiet without a garbled line; the caller holds c.mu
func (c *rxFrameCheck) maybeDismiss() {
	if c.notified && c.now().Sub(c.lastBad) >= garbledQuiet {
		c.notified = false
		go haDismiss(garbledNotifyID)
	}
}

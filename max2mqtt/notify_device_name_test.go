package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// useDeviceNames makes notifications run at once and take the device names from names
// (nil = the lookup fails); the name cache starts empty. Restored after the test.
func useDeviceNames(t *testing.T, names map[string]string) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	oldRun, oldNames := runNotify, haDeviceNames
	notificationGenerations.mu.Lock()
	oldGens := notificationGenerations.byID
	notificationGenerations.byID = map[string]uint64{}
	notificationGenerations.mu.Unlock()
	runNotify = func(f func()) { f() }
	haDeviceNames = func() (map[string]string, error) {
		calls.Add(1)
		if names == nil {
			return nil, errors.New("Home Assistant not reachable")
		}
		return names, nil
	}
	resetDeviceNames()
	t.Cleanup(func() {
		runNotify, haDeviceNames = oldRun, oldNames
		notificationGenerations.mu.Lock()
		notificationGenerations.byID = oldGens
		notificationGenerations.mu.Unlock()
		resetDeviceNames()
	})
	return calls
}

func resetDeviceNames() {
	deviceNames.mu.Lock()
	deviceNames.byID = nil
	deviceNames.fetched = time.Time{}
	deviceNames.attempted = time.Time{}
	deviceNames.mu.Unlock()
}

type sentNotification struct{ id, title, message string }

func captureNotifications(t *testing.T) *[]sentNotification {
	t.Helper()
	var got []sentNotification
	old := haNotify
	haNotify = func(id, title, message string) { got = append(got, sentNotification{id, title, message}) }
	t.Cleanup(func() { haNotify = old })
	return &got
}

func TestUnackedCommandNotificationNamesTheDevice(t *testing.T) {
	resetTestState()
	resetCommandAckSeen(t)
	useDeviceNames(t, map[string]string{"0A1B2C": "Living room"})
	got := captureNotifications(t)

	commandUnacked("0A1B2C", "Set Mode auto", commandFailure{Reason: failNoAck, Tries: 3})

	if len(*got) != 1 {
		t.Fatalf("expected one notification, got %d", len(*got))
	}
	n := (*got)[0]
	if n.id != "max2mqtt_command_unacked_0A1B2C" {
		t.Errorf("id changed: %q", n.id)
	}
	if !strings.HasSuffix(n.title, ": Living room") {
		t.Errorf("title without the device name: %q", n.title)
	}
	if !strings.HasPrefix(n.message, "MAX! device Living room (0A1B2C), command \"Set Mode auto\"") {
		t.Errorf("message without name and address: %q", n.message)
	}
}

func TestWeekProfileNotificationNamesTheDevice(t *testing.T) {
	resetTestState()
	useDeviceNames(t, map[string]string{"0A1B2C": "Living room"})
	got := captureNotifications(t)

	weekProfileDaysUnacked("0A1B2C", "monday", []int{2}, false)

	if len(*got) != 1 {
		t.Fatalf("expected one notification, got %d", len(*got))
	}
	n := (*got)[0]
	if n.title != "MAX! week profile not acknowledged: Living room" {
		t.Errorf("title: %q", n.title)
	}
	if !strings.HasPrefix(n.message, "MAX! device Living room (0A1B2C) did not acknowledge") ||
		!strings.Contains(n.message, "(max/0A1B2C/programme/set)") {
		t.Errorf("message: %q", n.message)
	}
}

func TestNotificationKeepsTheAddressWithoutName(t *testing.T) {
	resetTestState()
	resetCommandAckSeen(t)
	useDeviceNames(t, nil) // Home Assistant not reachable
	got := captureNotifications(t)

	commandUnacked("0A1B2C", "Set Mode auto", commandFailure{Reason: failNoAck, Tries: 3})

	n := (*got)[0]
	if n.title != (commandFailure{Reason: failNoAck}).title() {
		t.Errorf("title changed without a name: %q", n.title)
	}
	if !strings.HasPrefix(n.message, "MAX! device 0A1B2C, command") {
		t.Errorf("message: %q", n.message)
	}
}

func TestDeviceNamesAreCached(t *testing.T) {
	calls := useDeviceNames(t, map[string]string{"0A1B2C": "Living room"})
	for i := 0; i < 3; i++ {
		if got := deviceName(haDeviceNames, "0a1b2c"); got != "Living room" {
			t.Fatalf("name: %q", got)
		}
	}
	// an unknown address does not ask Home Assistant again right away
	if got := deviceName(haDeviceNames, "0D0E0F"); got != "" {
		t.Fatalf("unknown device got a name: %q", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected one lookup, got %d", n)
	}
}

func TestDeviceLabel(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"Living room", "Living room (0A1B2C)"},
		{"", "0A1B2C"},
		{"0a1b2c", "0A1B2C"},
		{"0A1B2C\\", "0A1B2C\\\\ (0A1B2C)"},
	} {
		if got := deviceLabel(c.name, "0A1B2C"); got != c.want {
			t.Errorf("deviceLabel(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseDeviceNames(t *testing.T) {
	names, err := parseDeviceNames(strings.NewReader(`{"0a1b2c":"Living room","0D0E0F":" ","BRIDGE":"MAX! Bridge"}`))
	if err != nil {
		t.Fatal(err)
	}
	if names["0A1B2C"] != "Living room" {
		t.Errorf("name not found by upper-case address: %v", names)
	}
	if _, ok := names["0D0E0F"]; ok {
		t.Errorf("blank name kept: %v", names)
	}
	if _, err := parseDeviceNames(strings.NewReader("not json")); err == nil {
		t.Error("expected an error for a broken answer")
	}
}

func TestFailedLookupIsNotRepeatedAtOnce(t *testing.T) {
	calls := useDeviceNames(t, nil)
	for i := 0; i < 3; i++ {
		if got := deviceName(haDeviceNames, "0A1B2C"); got != "" {
			t.Fatalf("name without Home Assistant: %q", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected one lookup after a failure, got %d", n)
	}
}

func TestDismissCancelsPendingCreate(t *testing.T) {
	useDeviceNames(t, map[string]string{"0A1B2C": "Living room"})
	got := captureNotifications(t)
	var pending func()
	runNotify = func(f func()) { pending = f } // the name lookup has not finished yet
	oldDismiss := haDismiss
	var dismissed []string
	haDismiss = func(id string) { dismissed = append(dismissed, id) }
	t.Cleanup(func() { haDismiss = oldDismiss })

	notifyDevice("max2mqtt_test_0A1B2C", "0A1B2C", func(name, label string) (string, string) { return "t", label })
	dismissNotification("max2mqtt_test_0A1B2C")
	pending()

	if len(*got) != 0 {
		t.Fatalf("create after the dismiss was sent: %v", *got)
	}
	if len(dismissed) != 1 {
		t.Fatalf("dismiss not sent: %v", dismissed)
	}
}

func TestDeviceNameIsEscapedInTheMessage(t *testing.T) {
	useDeviceNames(t, map[string]string{"0A1B2C": "Guest *room*"})
	got := captureNotifications(t)
	notifyDevice("max2mqtt_test_0A1B2C", "0A1B2C", func(name, label string) (string, string) {
		return withDeviceName("t", name), label
	})
	n := (*got)[0]
	if n.title != "t: Guest *room*" {
		t.Errorf("title: %q", n.title)
	}
	if n.message != `Guest \*room\* (0A1B2C)` {
		t.Errorf("message: %q", n.message)
	}
}

func TestEnqueueHACallKeepsPendingDismiss(t *testing.T) {
	haPending.mu.Lock()
	saved, savedOrder := haPending.calls, haPending.order
	haPending.calls, haPending.order = map[string][]map[string]string{}, nil
	haPending.mu.Unlock()
	t.Cleanup(func() {
		haPending.mu.Lock()
		haPending.calls, haPending.order = saved, savedOrder
		haPending.mu.Unlock()
	})
	services := func(id string) []string {
		haPending.mu.Lock()
		defer haPending.mu.Unlock()
		var out []string
		for _, c := range haPending.calls[id] {
			out = append(out, c["service"])
		}
		return out
	}
	id := "max2mqtt_test"
	enqueueHACall(id, map[string]string{"service": "create"})
	enqueueHACall(id, map[string]string{"service": "dismiss"})
	if got := services(id); len(got) != 1 || got[0] != "dismiss" {
		t.Fatalf("dismiss should replace create, got %v", got)
	}
	enqueueHACall(id, map[string]string{"service": "create", "message": "a"})
	enqueueHACall(id, map[string]string{"service": "create", "message": "b"})
	got := services(id)
	if len(got) != 2 || got[0] != "dismiss" || got[1] != "create" {
		t.Fatalf("waiting dismiss should stay ahead of the latest create, got %v", got)
	}
	haPending.mu.Lock()
	msg, n := haPending.calls[id][1]["message"], len(haPending.order)
	haPending.mu.Unlock()
	if msg != "b" || n != 1 {
		t.Fatalf("latest create should win once in order, got message %q, order %d", msg, n)
	}
}

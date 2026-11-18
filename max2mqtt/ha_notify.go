package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// bulkNotifyAfter is how long a bulk command (week profile) waits for duty cycle credits
// before the bridge raises a notification in Home Assistant. It keeps waiting afterwards.
var bulkNotifyAfter = 60 * time.Minute

// haNotify and haDismiss create or remove a persistent notification in Home Assistant.
// They are variables so tests can replace them.
var haNotify = supervisorNotify
var haDismiss = supervisorDismiss

// bulkWaitTracker counts the queued bulk commands per device and remembers the devices
// that got a "still waiting" notification, so it is removed once the transfer is done.
type bulkWaitTracker struct {
	mu       sync.Mutex
	pending  map[string]int
	notified map[string]bool
}

func newBulkWaitTracker() *bulkWaitTracker {
	return &bulkWaitTracker{pending: map[string]int{}, notified: map[string]bool{}}
}

func bulkNotificationID(deviceID string) string {
	return "max2mqtt_week_profile_" + deviceID
}

// added is called for every bulk command that entered the queue
func (b *bulkWaitTracker) added(deviceID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.pending[deviceID]++
	b.mu.Unlock()
}

// waiting raises the notification once per device when a command waits longer than bulkNotifyAfter
func (b *bulkWaitTracker) waiting(cmd QueuedCommand) {
	if b == nil || time.Since(cmd.CreatedAt) < bulkNotifyAfter {
		return
	}
	b.mu.Lock()
	if b.notified[cmd.DeviceID] {
		b.mu.Unlock()
		return
	}
	b.notified[cmd.DeviceID] = true
	b.mu.Unlock()
	slog.Warn("TxMgr: Week profile still waiting for duty cycle credits", "device", cmd.DeviceID, "since", cmd.CreatedAt.Format(time.TimeOnly))
	since := cmd.CreatedAt.Format("15:04")
	notifyDevice(bulkNotificationID(cmd.DeviceID), cmd.DeviceID, func(name, label string) (string, string) {
		return withDeviceName("MAX! week profile waiting", name),
			fmt.Sprintf("The week profile for MAX! device %s has been waiting for duty cycle credits since %s. It is not discarded and is sent as soon as there are enough credits; this notification disappears then.",
				label, since)
	})
}

// done is called when a bulk command was sent; the last one of a device removes the notification
func (b *bulkWaitTracker) done(cmd QueuedCommand) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.pending[cmd.DeviceID] > 0 {
		b.pending[cmd.DeviceID]--
	}
	finished := b.pending[cmd.DeviceID] == 0
	if finished {
		delete(b.pending, cmd.DeviceID)
	}
	wasNotified := b.notified[cmd.DeviceID]
	if finished {
		delete(b.notified, cmd.DeviceID)
	}
	b.mu.Unlock()

	if finished && wasNotified {
		dismissNotification(bulkNotificationID(cmd.DeviceID))
	}
}

// runNotify runs the name lookup and the notification about a device. It runs in the background,
// so a slow Home Assistant never holds up the dispatcher; tests replace it to run at once.
var runNotify = func(f func()) { go f() }

// notifyDevice creates a notification about one MAX! device. text gets the name Home Assistant
// shows for the device ("" when unknown) and the label "Name (0A1B2C)" (only the address when the
// name is unknown), so the user sees which thermostat is meant and not only its radio address.
//
// The lookup can take a while; a dismiss of the same notification in the meantime (dismissNotification)
// cancels the create, so a condition that has cleared is not shown afterwards.
func notifyDevice(id, deviceID string, text func(name, label string) (title, message string)) {
	notify, fetch := haNotify, haDeviceNames
	gen := notificationGenerations.next(id)
	runNotify(func() {
		name := deviceName(fetch, deviceID)
		title, message := text(name, deviceLabel(escapeMarkdown(name), deviceID))
		notificationGenerations.mu.Lock()
		defer notificationGenerations.mu.Unlock()
		if notificationGenerations.byID[id] != gen {
			return // dismissed or replaced while the name was looked up
		}
		notify(id, title, message)
	})
}

// notificationGenerations counts the creates and dismisses per notification ID
var notificationGenerations = &notificationGens{byID: map[string]uint64{}}

type notificationGens struct {
	mu   sync.Mutex
	byID map[string]uint64
}

func (g *notificationGens) next(id string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byID[id]++
	return g.byID[id]
}

// dismissNotification removes a notification and cancels a create of it that still waits for the
// device name. Use it for notifications created by notifyDevice.
func dismissNotification(id string) {
	notificationGenerations.mu.Lock()
	defer notificationGenerations.mu.Unlock()
	notificationGenerations.byID[id]++
	haDismiss(id)
}

// escapeMarkdown keeps a device name literal in the notification text, which Home Assistant
// renders as Markdown
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|<>~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// withDeviceName appends the device name to a notification title, if it is known
func withDeviceName(title, name string) string {
	if name == "" {
		return title
	}
	return title + ": " + name
}

// deviceLabel is "Name (0A1B2C)", or only the address when Home Assistant has no other name
func deviceLabel(name, deviceID string) string {
	if name == "" || strings.EqualFold(strings.Trim(name, "\\"), deviceID) {
		return deviceID
	}
	return name + " (" + deviceID + ")"
}

// haDeviceNames returns the names Home Assistant shows for the MAX! devices, keyed by radio
// address in upper case. A variable so tests can replace it.
var haDeviceNames = supervisorDeviceNames

const (
	// deviceNamesMaxAge: the names are fetched again after this long, so a renamed entity shows up
	deviceNamesMaxAge = 10 * time.Minute
	// deviceNamesRetry: an address missing from the names is looked up again after this long at the earliest
	deviceNamesRetry = time.Minute
)

var deviceNames struct {
	mu      sync.Mutex
	byID    map[string]string
	fetched time.Time
	// attempted: the last lookup, also a failed one; no new lookup within deviceNamesRetry
	attempted time.Time
}

// deviceName returns the name Home Assistant shows for the device, "" when unknown. The names are
// cached; a failed lookup only means the notification shows the address alone.
func deviceName(fetch func() (map[string]string, error), deviceID string) string {
	id := strings.ToUpper(deviceID)
	deviceNames.mu.Lock()
	defer deviceNames.mu.Unlock()
	name, ok := deviceNames.byID[id]
	age := time.Since(deviceNames.fetched)
	due := deviceNames.fetched.IsZero() || age >= deviceNamesMaxAge || (!ok && age >= deviceNamesRetry)
	if due && (deviceNames.attempted.IsZero() || time.Since(deviceNames.attempted) >= deviceNamesRetry) {
		deviceNames.attempted = time.Now()
		names, err := fetch()
		if err != nil {
			// not the error itself: it may contain an IP address
			var se templateStatusError
			if errors.As(err, &se) {
				slog.Debug("Device names from Home Assistant not available", "status", int(se))
			} else {
				slog.Debug("Device names from Home Assistant not available")
			}
			return name // the last names found stay
		}
		deviceNames.byID = names
		deviceNames.fetched = time.Now()
		name = names[id]
	}
	return name
}

// deviceNamesTemplate collects the names of the MAX! devices in Home Assistant: the name of the
// entity with the attribute device_rf_address (climate), otherwise the name the user gave the
// device, otherwise the shortest entity name of the device. A name that is just the address counts as none.
const deviceNamesTemplate = `{%- set ns = namespace(m={}, best='') -%}
{%- for s in states if s.attributes.device_rf_address is defined -%}
{%- set rf = s.attributes.device_rf_address | string | upper -%}
{%- set n = s.name | string | trim -%}
{%- if n and n | upper != rf -%}{%- set ns.m = dict(ns.m, **{rf: n}) -%}{%- endif -%}
{%- endfor -%}
{%- for e in integration_entities('mqtt') -%}
{%- set d = device_id(e) -%}
{%- if d -%}{%- for i in device_attr(d, 'identifiers') or [] -%}
{%- set k = i | list | last | string -%}
{%- set rf = k[4:] | upper -%}
{%- if k.startswith('max_') and rf not in ns.m -%}
{%- set ns.best = (device_attr(d, 'name_by_user') or '') | trim -%}
{%- if not ns.best -%}{%- for x in device_entities(d) -%}
{%- set n = state_attr(x, 'friendly_name') | string | trim -%}
{%- if n and n != 'None' and n | upper != rf and (not ns.best or n | length < ns.best | length) -%}{%- set ns.best = n -%}{%- endif -%}
{%- endfor -%}{%- endif -%}
{%- if ns.best -%}{%- set ns.m = dict(ns.m, **{rf: ns.best}) -%}{%- endif -%}
{%- endif -%}{%- endfor -%}{%- endif -%}
{%- endfor -%}
{{- ns.m | to_json -}}`

// supervisorDeviceNames renders deviceNamesTemplate through the Home Assistant Core API of the
// Supervisor. Without SUPERVISOR_TOKEN (bridge outside Home Assistant) there are no names.
func supervisorDeviceNames() (map[string]string, error) {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		return map[string]string{}, nil
	}
	body, _ := json.Marshal(map[string]string{"template": deviceNamesTemplate})
	req, err := http.NewRequest(http.MethodPost, "http://supervisor/core/api/template", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, templateStatusError(resp.StatusCode)
	}
	return parseDeviceNames(resp.Body)
}

// templateStatusError is an HTTP status of the template API other than success
type templateStatusError int

func (e templateStatusError) Error() string { return fmt.Sprintf("template API: HTTP %d", int(e)) }

// parseDeviceNames reads the rendered template, a JSON object of radio address → name
func parseDeviceNames(r io.Reader) (map[string]string, error) {
	var raw map[string]string
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	names := make(map[string]string, len(raw))
	for id, name := range raw {
		if name = strings.TrimSpace(name); name != "" {
			names[strings.ToUpper(id)] = name
		}
	}
	return names, nil
}

// supervisorNotify creates a persistent notification through the Home Assistant Core API of the
// Supervisor (needs homeassistant_api in config.yaml). Without SUPERVISOR_TOKEN (bridge running
// outside Home Assistant) only the log entry remains.
func supervisorNotify(id, title, message string) {
	callHAService("create", map[string]string{"notification_id": id, "title": title, "message": message})
}

func supervisorDismiss(id string) {
	callHAService("dismiss", map[string]string{"notification_id": id})
}

// haCalls runs the notification calls one after another in the order they were made, so a
// dismiss cannot overtake the create of the same notification
var (
	haCalls     chan func()
	haCallsOnce sync.Once
)

func callHAService(service string, data map[string]string) {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		slog.Debug("No SUPERVISOR_TOKEN, skipping Home Assistant notification", "service", service)
		return
	}
	haCallsOnce.Do(func() {
		haCalls = make(chan func(), 256)
		go func() {
			for call := range haCalls {
				call()
			}
		}()
	})
	haCalls <- func() {
		body, _ := json.Marshal(data)
		req, err := http.NewRequest(http.MethodPost, "http://supervisor/core/api/services/persistent_notification/"+service, bytes.NewReader(body))
		if err != nil {
			slog.Warn("Home Assistant notification failed", "error", err)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("Home Assistant notification failed", "error", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("Home Assistant notification failed", "status", resp.Status)
		}
	}
}

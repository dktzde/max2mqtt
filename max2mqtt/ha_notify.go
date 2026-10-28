package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	haNotify(bulkNotificationID(cmd.DeviceID), "MAX! week profile waiting",
		fmt.Sprintf("The week profile for MAX! device %s has been waiting for duty cycle credits since %s. It is not discarded and is sent as soon as there are enough credits; this notification disappears then.",
			cmd.DeviceID, cmd.CreatedAt.Format("15:04")))
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
		haDismiss(bulkNotificationID(cmd.DeviceID))
	}
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

func callHAService(service string, data map[string]string) {
	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		slog.Debug("No SUPERVISOR_TOKEN, skipping Home Assistant notification", "service", service)
		return
	}
	go func() {
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
	}()
}

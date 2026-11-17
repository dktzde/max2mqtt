package main

import (
	"sync"
	"time"
)

// Acknowledgement state of the config changes sent last, published in the climate attribute
// config_status ({"valve": {"state": "acknowledged", "field": "boost_duration",
// "at": "2026-11-10T06:23:39.898+01:00"}, ...}), one entry per kind of config (temperatures, valve,
// group, display). The config values themselves are shown at once (optimistically), so Home
// Assistant needs this to wait until the device really took a change, e.g. a new boost valve
// position before starting a boost. Not persisted.

// configStatusTimeLayout is RFC 3339 with milliseconds: an Ack comes about 2 s after sending, and
// Home Assistant compares the time with the moment it sent the change
const configStatusTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// ConfigStatus is the acknowledgement state of the last change of one kind of config; a failed change (state not_acknowledged or not_sent) also says why:
// reason (see failReason, e.g. "no_ack" or "duty_cycle") and detail (a short English text).
type ConfigStatus struct {
	State  string `json:"state"`
	Field  string `json:"field"`
	At     string `json:"at"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

var (
	configStatusMu sync.Mutex
	configStatus   = map[string]map[string]ConfigStatus{}
)

// setConfigStatus records the state of the last change of a kind of config. The attributes are
// published by the caller (publishDeviceConfig).
func setConfigStatus(deviceID, kind, field, state string) {
	configStatusMu.Lock()
	defer configStatusMu.Unlock()
	m := configStatus[deviceID]
	if m == nil {
		m = map[string]ConfigStatus{}
		configStatus[deviceID] = m
	}
	m[kind] = ConfigStatus{State: state, Field: field, At: time.Now().Format(configStatusTimeLayout)}
}

// setConfigStatusFailed records a change that did not reach the device and why
func setConfigStatusFailed(deviceID, kind, field string, f commandFailure) {
	configStatusMu.Lock()
	defer configStatusMu.Unlock()
	m := configStatus[deviceID]
	if m == nil {
		m = map[string]ConfigStatus{}
		configStatus[deviceID] = m
	}
	m[kind] = ConfigStatus{State: f.status(), Field: field, At: time.Now().Format(configStatusTimeLayout),
		Reason: string(f.Reason), Detail: f.summary()}
}

// configStatusOf returns a copy of the config states of a device, nil when nothing was sent yet
func configStatusOf(deviceID string) map[string]ConfigStatus {
	configStatusMu.Lock()
	defer configStatusMu.Unlock()
	m := configStatus[deviceID]
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]ConfigStatus, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

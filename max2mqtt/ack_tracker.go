package main

import (
	"log/slog"
	"sync"
	"time"
)

const configAckTimeout = 30 * time.Second

// PendingConfigAck tracks an outstanding config TX until the device Acks (CUL push).
type PendingConfigAck struct {
	DeviceID string
	MsgType  byte
	Field    string
	Previous DeviceConfigSnapshot
	Next     DeviceConfigSnapshot
	Timer    *time.Timer
}

// DeviceConfigSnapshot is a copy of config used for optimistic update / rollback.
type DeviceConfigSnapshot struct {
	Temps   DeviceConfig
	Valve   ValveConfig
	Display string
	GroupID int
}

// PendingSetpoint tracks SetTemperature/mode until the device Acks (even short 0x02).
// FHEM applies desiredTemperature from the Ack callback — short Acks have no temp payload.
type PendingSetpoint struct {
	Temperature float64
	Mode        string
	HVACMode    string
	Until       string // vacation end "YYYY-MM-DD HH:MM", empty for other modes
}

type AckTracker struct {
	mutex          sync.Mutex
	pending        map[string]*PendingConfigAck // key: deviceID
	pendingSetTemp map[string]*PendingSetpoint
}

var (
	ackTracker     *AckTracker
	lastSeenMu     sync.Mutex
	lastSeenDevice = map[string]time.Time{}
)

func initAckTracker() {
	ackTracker = &AckTracker{
		pending:        make(map[string]*PendingConfigAck),
		pendingSetTemp: make(map[string]*PendingSetpoint),
	}
	slog.Info("AckTracker initialized", "timeout", configAckTimeout)
}

func touchLastSeen(deviceID string) {
	lastSeenMu.Lock()
	lastSeenDevice[deviceID] = time.Now()
	lastSeenMu.Unlock()
}

func deviceNeedsWakeUp(deviceID string) bool {
	lastSeenMu.Lock()
	defer lastSeenMu.Unlock()
	t, ok := lastSeenDevice[deviceID]
	return !ok || time.Since(t) > 45*time.Second
}

// TrackSetpoint remembers the expected climate state until an Ack arrives from the device.
func (a *AckTracker) TrackSetpoint(deviceID string, temp float64, mode, hvacMode string) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	a.pendingSetTemp[deviceID] = &PendingSetpoint{Temperature: temp, Mode: mode, HVACMode: hvacMode}
	a.mutex.Unlock()
	slog.Debug("Tracking pending SetTemperature", "device", deviceID, "temp", temp, "mode", mode, "hvac", hvacMode)
}

// TrackVacation remembers an expected vacation state (temperature until a date/time) until the device Acks.
func (a *AckTracker) TrackVacation(deviceID string, temp float64, until string) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	a.pendingSetTemp[deviceID] = &PendingSetpoint{Temperature: temp, Mode: "vacation", HVACMode: "off", Until: until}
	a.mutex.Unlock()
	slog.Debug("Tracking pending vacation", "device", deviceID, "temp", temp, "until", until)
}

// ApplySetpointAck applies pending setpoint on any 0x02 Ack from the device (FHEM AckSetTemperature).
// Returns true if a pending setpoint was applied.
func (a *AckTracker) ApplySetpointAck(deviceID string) bool {
	if a == nil {
		return false
	}
	a.mutex.Lock()
	pending, ok := a.pendingSetTemp[deviceID]
	if !ok {
		a.mutex.Unlock()
		return false
	}
	delete(a.pendingSetTemp, deviceID)
	temp, mode, hvac, until := pending.Temperature, pending.Mode, pending.HVACMode, pending.Until
	a.mutex.Unlock()

	slog.Info("SetTemperature Ack — applying expected state", "device", deviceID, "temp", temp, "mode", mode)

	stateMutex.Lock()
	existing, exists := stateCache[deviceID]
	if !exists {
		existing = &MaxDeviceData{}
		stateCache[deviceID] = existing
	}
	existing.Temperature = temp
	if mode != "" {
		existing.Mode = mode
	}
	if hvac != "" {
		existing.HVACMode = hvac
	}
	existing.Until = until
	if existing.Mode == "boost" {
		existing.PresetMode = "boost"
	} else if existing.PresetMode == "boost" {
		existing.PresetMode = "none"
	}
	pub := *existing
	stateMutex.Unlock()

	if verificationMgr != nil {
		verificationMgr.CheckVerification(deviceID, temp)
	}
	publishState(deviceID, &pub)
	return true
}

// TrackConfig registers a pending config change. Optimistically applies Next and publishes.
// On Ack from device, confirms; on timeout, restores Previous.
func (a *AckTracker) TrackConfig(deviceID string, msgType byte, field string, prev, next DeviceConfigSnapshot) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	if old, ok := a.pending[deviceID]; ok && old.Timer != nil {
		old.Timer.Stop()
	}
	entry := &PendingConfigAck{
		DeviceID: deviceID,
		MsgType:  msgType,
		Field:    field,
		Previous: prev,
		Next:     next,
	}
	entry.Timer = time.AfterFunc(configAckTimeout, func() {
		a.handleTimeout(deviceID)
	})
	a.pending[deviceID] = entry
	a.mutex.Unlock()

	applyConfigSnapshot(deviceID, next)
	publishDeviceConfig(deviceID)
	slog.Info("Config change pending Ack", "device", deviceID, "field", field, "type", msgType)
}

// Confirm marks a successful device Ack/status push and cancels the timeout.
func (a *AckTracker) Confirm(deviceID string) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	entry, ok := a.pending[deviceID]
	if !ok {
		a.mutex.Unlock()
		slog.Debug("Ack Confirm: no pending config for device", "device", deviceID)
		return
	}
	if entry.Timer != nil {
		entry.Timer.Stop()
	}
	delete(a.pending, deviceID)
	field := entry.Field
	a.mutex.Unlock()

	// Authoritative publish after Ack (cache already has Next from optimistic apply)
	publishDeviceConfig(deviceID)
	slog.Info("Config Ack confirmed", "device", deviceID, "field", field)
}

func (a *AckTracker) handleTimeout(deviceID string) {
	a.mutex.Lock()
	entry, ok := a.pending[deviceID]
	if !ok {
		a.mutex.Unlock()
		return
	}
	prev := entry.Previous
	field := entry.Field
	delete(a.pending, deviceID)
	a.mutex.Unlock()

	slog.Warn("Config Ack timeout; restoring previous values", "device", deviceID, "field", field)
	applyConfigSnapshot(deviceID, prev)
	publishDeviceConfig(deviceID)
}

func applyConfigSnapshot(deviceID string, snap DeviceConfigSnapshot) {
	cfg := getDeviceConfig(deviceID)
	valve := getValveConfig(deviceID)
	configMutex.Lock()
	*cfg = snap.Temps
	*valve = snap.Valve
	cfg.DisplayMode = snap.Display
	cfg.GroupID = snap.GroupID
	configMutex.Unlock()
}

func snapshotDevice(deviceID string) DeviceConfigSnapshot {
	cfg := getDeviceConfig(deviceID)
	valve := getValveConfig(deviceID)
	configMutex.RLock()
	defer configMutex.RUnlock()
	return DeviceConfigSnapshot{
		Temps:   *cfg,
		Valve:   *valve,
		Display: cfg.DisplayMode,
		GroupID: cfg.GroupID,
	}
}

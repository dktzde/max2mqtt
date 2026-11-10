package main

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// configAckTimeout is the safety net for a config change whose telegram never reached the end of
// its tries (normally ConfigUnacked rolls it back after the last try). It runs from the moment the
// change is tracked, so it is added to the command timeout (credit wait) and must be longer than
// the tries of all telegrams queued before it (each try waits up to bulkAckTimeout for the Ack).
const configAckTimeout = 2 * time.Minute

// configRollbackAfter returns when an unconfirmed config change is rolled back at the latest
func configRollbackAfter() time.Duration {
	if txMgr != nil {
		return configAckTimeout + txMgr.Timeout
	}
	return configAckTimeout
}

// PendingConfigAck tracks an outstanding config TX until the device Acks (CUL push).
type PendingConfigAck struct {
	DeviceID string
	MsgType  byte
	Field    string
	Previous DeviceConfigSnapshot
	Next     DeviceConfigSnapshot
	Timer    *time.Timer
	// Counters holds the message counters of the telegrams sent for this change (see BindCounter).
	// Only an Ack carrying one of them confirms the change.
	Counters map[int]bool
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
	// Counters holds the message counters of the SetTemperature telegrams sent for this setpoint
	// (first try and retries). Only an Ack carrying one of them applies the setpoint.
	Counters map[int]bool
	// Previous is the climate state from before the first unacknowledged command (nil when the
	// device was unknown); it is shown again when the device does not acknowledge the command.
	Previous *climateSnapshot
	// StatusSeen is set when the device reported its state while the command was pending; the
	// state cache then holds the device's real state and Previous is not used.
	StatusSeen bool
}

// climateSnapshot holds the climate fields a SetTemperature command changes optimistically
type climateSnapshot struct {
	Temperature         float64
	Mode, HVACMode      string
	PresetMode, Until   string
	ModeBeforeBoost     string
	SetpointBeforeBoost float64
}

func takeClimateSnapshot(deviceID string) *climateSnapshot {
	stateMutex.RLock()
	defer stateMutex.RUnlock()
	d, ok := stateCache[deviceID]
	if !ok {
		return nil
	}
	return &climateSnapshot{
		Temperature: d.Temperature, Mode: d.Mode, HVACMode: d.HVACMode,
		PresetMode: d.PresetMode, Until: d.Until,
		ModeBeforeBoost: d.ModeBeforeBoost, SetpointBeforeBoost: d.SetpointBeforeBoost,
	}
}

func (c *climateSnapshot) applyTo(d *MaxDeviceData) {
	d.Temperature, d.Mode, d.HVACMode = c.Temperature, c.Mode, c.HVACMode
	d.PresetMode, d.Until = c.PresetMode, c.Until
	d.ModeBeforeBoost, d.SetpointBeforeBoost = c.ModeBeforeBoost, c.SetpointBeforeBoost
}

type AckTracker struct {
	mutex          sync.Mutex
	pending        map[string]map[string]*PendingConfigAck // key: deviceID, then configKind
	pendingSetTemp map[string]*PendingSetpoint
}

var (
	ackTracker     *AckTracker
	lastSeenMu     sync.Mutex
	lastSeenDevice = map[string]time.Time{}
	// lastWakeUpQueued holds when a WakeUp was last queued per device (guarded by lastSeenMu)
	lastWakeUpQueued = map[string]time.Time{}
)

func initAckTracker() {
	ackTracker = &AckTracker{
		pending:        make(map[string]map[string]*PendingConfigAck),
		pendingSetTemp: make(map[string]*PendingSetpoint),
	}
	slog.Info("AckTracker initialized", "timeout", configAckTimeout)
}

func touchLastSeen(deviceID string) {
	lastSeenMu.Lock()
	lastSeenDevice[deviceID] = time.Now()
	lastSeenMu.Unlock()
}

// wakeUpWindow is how long a device counts as awake after it was heard or after a WakeUp was queued
const wakeUpWindow = 45 * time.Second

func deviceNeedsWakeUp(deviceID string) bool {
	lastSeenMu.Lock()
	defer lastSeenMu.Unlock()
	t, ok := lastSeenDevice[deviceID]
	return !ok || time.Since(t) > wakeUpWindow
}

// markWakeUpQueued records a WakeUp for the device and reports false when one was already queued
// within wakeUpWindow, so the telegrams of one command get a single WakeUp in front of them.
func markWakeUpQueued(deviceID string) bool {
	lastSeenMu.Lock()
	defer lastSeenMu.Unlock()
	if t, ok := lastWakeUpQueued[deviceID]; ok && time.Since(t) <= wakeUpWindow {
		return false
	}
	lastWakeUpQueued[deviceID] = time.Now()
	return true
}

// TrackSetpoint remembers the expected climate state until an Ack arrives from the device.
func (a *AckTracker) TrackSetpoint(deviceID string, temp float64, mode, hvacMode string) {
	if a == nil {
		return
	}
	a.trackPending(deviceID, &PendingSetpoint{Temperature: temp, Mode: mode, HVACMode: hvacMode})
	slog.Debug("Tracking pending SetTemperature", "device", deviceID, "temp", temp, "mode", mode, "hvac", hvacMode)
}

// trackPending stores a pending setpoint. Called before the command changes the state cache
// optimistically, so the state from before the command can be shown again without an Ack. While
// an earlier command is still unacknowledged, the state from before that one is kept.
func (a *AckTracker) trackPending(deviceID string, p *PendingSetpoint) {
	p.Previous = takeClimateSnapshot(deviceID)
	a.mutex.Lock()
	if old, ok := a.pendingSetTemp[deviceID]; ok && !old.StatusSeen {
		p.Previous = old.Previous
	}
	a.pendingSetTemp[deviceID] = p
	a.mutex.Unlock()
}

// StatusReceived notes that the device reported its state (status message or Ack with state), so
// a pending command that is not acknowledged later does not bring back the state from before it.
func (a *AckTracker) StatusReceived(deviceID string) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	if p, ok := a.pendingSetTemp[deviceID]; ok {
		p.StatusSeen = true
	}
	a.mutex.Unlock()
}

// SetpointUnacked is called when the device did not acknowledge the SetTemperature telegram with
// message counter cnt after all tries. It forgets the pending setpoint and publishes the state from
// before the command (or the last state the device reported since). Returns false when the
// telegram does not belong to the pending setpoint (already acknowledged or replaced).
func (a *AckTracker) SetpointUnacked(deviceID string, cnt int) bool {
	if a == nil {
		return false
	}
	a.mutex.Lock()
	p, ok := a.pendingSetTemp[deviceID]
	if !ok || !p.Counters[cnt] {
		a.mutex.Unlock()
		return false
	}
	delete(a.pendingSetTemp, deviceID)
	a.mutex.Unlock()

	stateMutex.Lock()
	existing, exists := stateCache[deviceID]
	if !exists {
		stateMutex.Unlock()
		return true
	}
	if !p.StatusSeen && p.Previous != nil {
		p.Previous.applyTo(existing)
		updateHVACAction(existing)
	}
	pub := *existing
	stateMutex.Unlock()
	slog.Warn("SetTemperature not acknowledged; showing the device's last known state", "device", deviceID, "mode", pub.Mode, "temp", pub.Temperature)
	publishState(deviceID, &pub)
	return true
}

// TrackVacation remembers an expected vacation state (temperature until a date/time) until the device Acks.
func (a *AckTracker) TrackVacation(deviceID string, temp float64, until string) {
	if a == nil {
		return
	}
	a.trackPending(deviceID, &PendingSetpoint{Temperature: temp, Mode: "vacation", HVACMode: "off", Until: until})
	slog.Debug("Tracking pending vacation", "device", deviceID, "temp", temp, "until", until)
}

// configMsgTypeMatches reports whether a sent message type belongs to a pending config change
// of the tracked type. Removing the group ID (0x23) is tracked as a group ID change (0x22).
func configMsgTypeMatches(tracked, sent byte) bool {
	return tracked == sent || (tracked == 0x22 && sent == 0x23)
}

// BindCounter records the message counter of a telegram sent to a device, so the Ack for exactly
// this telegram can be recognised later. A device answers every telegram with an Ack that carries
// the counter of that telegram; Acks for other telegrams (for example the previous week profile
// day, or the WakeUp sent before a SetTemperature) must not confirm a pending change (FHEM
// 14_CUL_MAX.pm matches src, dst and counter in the same way).
func (a *AckTracker) BindCounter(deviceID string, msgType byte, cnt int) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if msgType == 0x40 {
		if p, ok := a.pendingSetTemp[deviceID]; ok {
			if p.Counters == nil {
				p.Counters = map[int]bool{}
			}
			p.Counters[cnt] = true
		}
		return
	}
	if entry, ok := a.pending[deviceID][configKind(msgType)]; ok && configMsgTypeMatches(entry.MsgType, msgType) {
		if entry.Counters == nil {
			entry.Counters = map[int]bool{}
		}
		entry.Counters[cnt] = true
	}
}

// ApplySetpointAck applies the pending setpoint when the device Acks the SetTemperature telegram
// (FHEM AckSetTemperature). cnt is the message counter of the Ack.
// Returns true if a pending setpoint was applied.
func (a *AckTracker) ApplySetpointAck(deviceID string, cnt int) bool {
	if a == nil {
		return false
	}
	a.mutex.Lock()
	pending, ok := a.pendingSetTemp[deviceID]
	if !ok {
		a.mutex.Unlock()
		return false
	}
	if !pending.Counters[cnt] {
		a.mutex.Unlock()
		slog.Debug("Ack is not for the pending SetTemperature", "device", deviceID, "cnt", cnt)
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
	// 0 = week program temperature (auto): not known until the next status message
	if temp > 0 {
		existing.Temperature = temp
	}
	if mode == "boost" && existing.Mode != "boost" {
		existing.ModeBeforeBoost, existing.SetpointBeforeBoost = existing.Mode, existing.Temperature
	}
	if mode != "" {
		existing.Mode = mode
	}
	if hvac != "" {
		existing.HVACMode = hvac
	}
	existing.Until = until
	clearActualTemperatureInVacation(existing)
	updateHVACAction(existing)
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

// configKind names the part of the device config a telegram type writes. Changes of different
// kinds (config/set writes temperatures and valve) are tracked, acknowledged and rolled back
// independently of each other.
func configKind(msgType byte) string {
	switch msgType {
	case 0x11:
		return "temperatures"
	case 0x12:
		return "valve"
	case 0x22, 0x23:
		return "group"
	case 0x82:
		return "display"
	}
	return fmt.Sprintf("type_%02x", msgType)
}

// pendingConfig returns the pending config changes of a device; the caller holds a.mutex
func (a *AckTracker) pendingConfig(deviceID string) map[string]*PendingConfigAck {
	m := a.pending[deviceID]
	if m == nil {
		m = map[string]*PendingConfigAck{}
		a.pending[deviceID] = m
	}
	return m
}

// removePending removes entry if it is still the pending change of its kind; the caller holds
// a.mutex. It reports false for an entry that was confirmed, rolled back or replaced meanwhile.
func (a *AckTracker) removePending(entry *PendingConfigAck) bool {
	m := a.pending[entry.DeviceID]
	kind := configKind(entry.MsgType)
	if m[kind] != entry {
		return false
	}
	if entry.Timer != nil {
		entry.Timer.Stop()
	}
	delete(m, kind)
	if len(m) == 0 {
		delete(a.pending, entry.DeviceID)
	}
	return true
}

// TrackConfig registers a pending config change. Optimistically applies Next and publishes.
// On Ack from device, confirms; on timeout, restores Previous.
func (a *AckTracker) TrackConfig(deviceID string, msgType byte, field string, prev, next DeviceConfigSnapshot) {
	if a == nil {
		return
	}
	kind := configKind(msgType)
	entry := &PendingConfigAck{
		DeviceID: deviceID,
		MsgType:  msgType,
		Field:    field,
		Previous: prev,
		Next:     next,
	}
	a.mutex.Lock()
	m := a.pendingConfig(deviceID)
	if old, ok := m[kind]; ok && old.Timer != nil {
		old.Timer.Stop()
	}
	entry.Timer = time.AfterFunc(configRollbackAfter(), func() {
		a.rollbackConfig(entry, "Config Ack timeout; restoring previous values")
	})
	m[kind] = entry
	a.mutex.Unlock()

	setConfigStatus(deviceID, kind, field, statusPending)
	applyConfigSnapshot(deviceID, next)
	publishDeviceConfig(deviceID)
	slog.Info("Config change pending Ack", "device", deviceID, "field", field, "type", msgType)
}

// Confirm marks a pending config change as acknowledged and cancels the timeout, but only when
// the Ack carries the message counter of a telegram sent for that change. cnt is the message
// counter of the Ack.
func (a *AckTracker) Confirm(deviceID string, cnt int) {
	if a == nil {
		return
	}
	a.mutex.Lock()
	entry := a.pendingByCounter(deviceID, cnt)
	if entry == nil {
		a.mutex.Unlock()
		slog.Debug("Ack Confirm: no pending config for this message", "device", deviceID, "cnt", cnt)
		return
	}
	a.removePending(entry)
	a.mutex.Unlock()

	setConfigStatus(deviceID, configKind(entry.MsgType), entry.Field, statusAcknowledged)
	// Authoritative publish after Ack (cache already has Next from optimistic apply)
	publishDeviceConfig(deviceID)
	slog.Info("Config Ack confirmed", "device", deviceID, "field", entry.Field)
}

// pendingByCounter returns the pending config change a telegram with message counter cnt was sent
// for, or nil; the caller holds a.mutex
func (a *AckTracker) pendingByCounter(deviceID string, cnt int) *PendingConfigAck {
	for _, entry := range a.pending[deviceID] {
		if entry.Counters[cnt] {
			return entry
		}
	}
	return nil
}

// rollbackConfig restores the values from before a change that was not acknowledged, unless the
// change was confirmed, rolled back or replaced meanwhile. Only the kind of config the change
// wrote is restored, so a pending change of another kind keeps its values.
func (a *AckTracker) rollbackConfig(entry *PendingConfigAck, msg string) bool {
	a.mutex.Lock()
	ok := a.removePending(entry)
	a.mutex.Unlock()
	if !ok {
		return false
	}
	slog.Warn(msg, "device", entry.DeviceID, "field", entry.Field)
	setConfigStatus(entry.DeviceID, configKind(entry.MsgType), entry.Field, statusNotAcknowledged)
	applyConfigKind(entry.DeviceID, entry.Previous, entry.MsgType)
	publishDeviceConfig(entry.DeviceID)
	return true
}

// ConfigUnacked is called when the device did not acknowledge the config telegram with message
// counter cnt after all tries: the previous values come back at once instead of after
// configAckTimeout. Returns false when the telegram does not belong to a pending change.
func (a *AckTracker) ConfigUnacked(deviceID string, cnt int) bool {
	if a == nil {
		return false
	}
	a.mutex.Lock()
	entry := a.pendingByCounter(deviceID, cnt)
	a.mutex.Unlock()
	if entry == nil {
		return false
	}
	return a.rollbackConfig(entry, "Config not acknowledged; restoring previous values")
}

// applyConfigKind restores the part of the config that a telegram of msgType writes
func applyConfigKind(deviceID string, snap DeviceConfigSnapshot, msgType byte) {
	cfg := getDeviceConfig(deviceID)
	switch configKind(msgType) {
	case "temperatures":
		configMutex.Lock()
		cfg.ComfortTemp, cfg.EcoTemp = snap.Temps.ComfortTemp, snap.Temps.EcoTemp
		cfg.MaxTemp, cfg.MinTemp = snap.Temps.MaxTemp, snap.Temps.MinTemp
		cfg.MeasurementOffset = snap.Temps.MeasurementOffset
		cfg.WindowOpenTemp, cfg.WindowOpenDuration = snap.Temps.WindowOpenTemp, snap.Temps.WindowOpenDuration
		cfg.TempsKnown = snap.Temps.TempsKnown
		configMutex.Unlock()
	case "valve":
		valve := getValveConfig(deviceID)
		valveMutex.Lock()
		*valve = snap.Valve
		valveMutex.Unlock()
	case "group":
		configMutex.Lock()
		cfg.GroupID = snap.GroupID
		configMutex.Unlock()
	case "display":
		configMutex.Lock()
		cfg.DisplayMode = snap.Display
		configMutex.Unlock()
	default:
		applyConfigSnapshot(deviceID, snap)
	}
}

// applyConfigSnapshot writes the config values of a snapshot into the caches. The valve config is
// guarded by valveMutex like everywhere else (publishDeviceConfig, attributes), not by configMutex.
func applyConfigSnapshot(deviceID string, snap DeviceConfigSnapshot) {
	cfg := getDeviceConfig(deviceID)
	valve := getValveConfig(deviceID)
	configMutex.Lock()
	*cfg = snap.Temps
	cfg.DisplayMode = snap.Display
	cfg.GroupID = snap.GroupID
	configMutex.Unlock()
	valveMutex.Lock()
	*valve = snap.Valve
	valveMutex.Unlock()
}

func snapshotDevice(deviceID string) DeviceConfigSnapshot {
	cfg := getDeviceConfig(deviceID)
	valve := getValveConfig(deviceID)
	configMutex.RLock()
	snap := DeviceConfigSnapshot{
		Temps:   *cfg,
		Display: cfg.DisplayMode,
		GroupID: cfg.GroupID,
	}
	configMutex.RUnlock()
	valveMutex.RLock()
	snap.Valve = *valve
	valveMutex.RUnlock()
	return snap
}

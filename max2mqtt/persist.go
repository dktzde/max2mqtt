package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Device data survives a bridge restart in /data/devices.json (the add-on's persistent folder):
// last state (mode, setpoint, ...), valve position, room/group ID, device type, the config values
// and whether they are known, and the week profile. MAX! devices cannot be asked for any of this,
// so without it a restart lost e.g. the mode (a setpoint change then switched auto to manual).
// The file is written at most every saveInterval when something changed, and on shutdown.
// Standalone (Docker): set DEVICE_DATA_FILE to another path if /data is not persistent.

const saveInterval = 30 * time.Second

var deviceDataFile = "/data/devices.json"

type persistedDevice struct {
	State         *MaxDeviceData `json:"state,omitempty"`
	ValvePosition *int           `json:"valve_position,omitempty"`
	Config        *DeviceConfig  `json:"config,omitempty"`
	Valve         *ValveConfig   `json:"valve,omitempty"`
	Programme     Programme      `json:"programme,omitempty"`
}

var lastSaved []byte

// snapshotDeviceData collects all caches into one serializable map
func snapshotDeviceData() map[string]*persistedDevice {
	out := map[string]*persistedDevice{}
	get := func(id string) *persistedDevice {
		if out[id] == nil {
			out[id] = &persistedDevice{}
		}
		return out[id]
	}
	stateMutex.RLock()
	for id, st := range stateCache {
		c := *st
		if st.CurrentTemperature != nil {
			v := *st.CurrentTemperature
			c.CurrentTemperature = &v
		}
		if st.ValvePosition != nil {
			v := *st.ValvePosition
			get(id).ValvePosition = &v
		}
		c.ValvePosition = nil
		get(id).State = &c
	}
	stateMutex.RUnlock()
	configMutex.RLock()
	for id, cfg := range deviceConfigCache {
		c := *cfg
		get(id).Config = &c
	}
	configMutex.RUnlock()
	valveMutex.RLock()
	for id, v := range valveConfigCache {
		c := *v
		get(id).Valve = &c
	}
	valveMutex.RUnlock()
	programmeMu.Lock()
	for id, prog := range programmes {
		p := Programme{}
		for d, s := range prog {
			p[d] = append([]ProgrammeSlot(nil), s...)
		}
		get(id).Programme = p
	}
	programmeMu.Unlock()
	return out
}

// saveDeviceData writes the device data if it changed since the last save
func saveDeviceData() {
	data, err := json.MarshalIndent(snapshotDeviceData(), "", "  ")
	if err != nil {
		slog.Error("Failed to serialize device data", "error", err)
		return
	}
	if bytes.Equal(data, lastSaved) {
		return
	}
	tmp := deviceDataFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(deviceDataFile), 0o755); err != nil {
		slog.Warn("Cannot save device data", "file", deviceDataFile, "error", err)
		return
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("Cannot save device data", "file", deviceDataFile, "error", err)
		return
	}
	if err := os.Rename(tmp, deviceDataFile); err != nil {
		slog.Warn("Cannot save device data", "file", deviceDataFile, "error", err)
		return
	}
	lastSaved = data
	slog.Debug("Saved device data", "file", deviceDataFile, "bytes", len(data))
}

// loadDeviceData fills the caches from the file (before MQTT and CUL start)
func loadDeviceData() {
	if f := os.Getenv("DEVICE_DATA_FILE"); f != "" {
		deviceDataFile = f
	}
	data, err := os.ReadFile(deviceDataFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("Cannot read device data", "file", deviceDataFile, "error", err)
		}
		return
	}
	var devices map[string]*persistedDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		slog.Error("Device data file is damaged, starting without it", "file", deviceDataFile, "error", err)
		return
	}
	for id, d := range devices {
		if d.State != nil {
			d.State.ValvePosition = d.ValvePosition
			stateMutex.Lock()
			stateCache[id] = d.State
			stateMutex.Unlock()
		}
		if d.Config != nil {
			configMutex.Lock()
			deviceConfigCache[id] = d.Config
			configMutex.Unlock()
		}
		if d.Valve != nil {
			valveMutex.Lock()
			valveConfigCache[id] = d.Valve
			valveMutex.Unlock()
		}
		if len(d.Programme) > 0 {
			programmeMu.Lock()
			programmes[id] = d.Programme
			programmeMu.Unlock()
		}
	}
	lastSaved = data
	slog.Info("Loaded device data", "file", deviceDataFile, "devices", len(devices))
}

// startDeviceDataSaver saves changes regularly
func startDeviceDataSaver() {
	for {
		time.Sleep(saveInterval)
		saveDeviceData()
	}
}

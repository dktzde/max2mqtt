package main

import (
	"path/filepath"
	"testing"
)

func TestDeviceDataSurvivesRestart(t *testing.T) {
	resetTestState()
	deviceDataFile = filepath.Join(t.TempDir(), "devices.json")
	t.Setenv("DEVICE_DATA_FILE", deviceDataFile)
	lastSaved = nil
	const dev = "0A1B2C"

	// Auto 13 °C, valve 20 %, room 3, known config, a week profile day
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 20, 26, 0, 140}, true))
	markDeviceType(dev, 0x60)
	updateDeviceGroupID(dev, 3)
	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{
		"comfort_temp": 16, "eco_temp": 12, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30}`})
	handleMQTTProgrammeImport(nil, fakeMessage{topic: "max/" + dev + "/programme/import", payload: `{"monday":[{"temp":9,"until":"05:20"},{"temp":18,"until":"24:00"}]}`})
	saveDeviceData()

	// "Restart": empty caches, load the file
	resetTestState()
	programmeMu.Lock()
	programmes = map[string]Programme{}
	programmeMu.Unlock()
	valveMutex.Lock()
	valveConfigCache = map[string]*ValveConfig{}
	valveMutex.Unlock()
	loadDeviceData()

	st := stateCache[dev]
	if st == nil || st.Mode != "auto" || st.Temperature != 13 || st.ValvePosition == nil || *st.ValvePosition != 20 {
		t.Fatalf("state after restart: %+v", st)
	}
	cfg := getDeviceConfig(dev)
	if !cfg.TempsKnown || cfg.EcoTemp != 12 || cfg.GroupID != 3 || !cfg.IsRadiator {
		t.Fatalf("config after restart: %+v", cfg)
	}
	if getModeFromCache(dev) != 0x00 {
		t.Fatal("mode not restored: a setpoint change would switch to manual")
	}
	if len(programmes[dev]["monday"]) != 2 {
		t.Fatalf("programme after restart: %+v", programmes[dev])
	}
}

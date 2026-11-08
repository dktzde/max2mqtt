package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUntilEncoding(t *testing.T) {
	// Example from the MAX! protocol docs: 9D 0B 10 = 29.08.2011 08:00
	if got := decodeUntil([]byte{0x9D, 0x0B, 0x10}); got != "2011-08-29 08:00" {
		t.Errorf("decode 9D0B10 = %q", got)
	}
	cases := []string{"2026-11-02 18:30", "2026-01-01 00:00", "2027-12-31 23:30", "2026-07-15 12:00"}
	for _, c := range cases {
		tm, _ := time.ParseInLocation(untilLayout, c, time.Local)
		if got := decodeUntil(encodeUntil(tm)); got != c {
			t.Errorf("round trip %s = %s", c, got)
		}
	}
}

func TestParseVacationCommand(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	cases := []struct {
		payload   string
		wantTemp  float64
		wantUntil string // empty = error expected
	}{
		{`{"temperature": 12, "until": "2026-11-02 18:00"}`, 12, "2026-11-02 18:00"},
		{`{"until": "2026-11-02T18:45"}`, 17, "2026-11-02 18:30"},                           // default temp, rounded down
		{`{"temperature": 12.3, "until": "2026-11-02 18:00:00"}`, 12.5, "2026-11-02 18:00"}, // HA datetime helper format
		{`{"temperature": 12, "until": "2026-10-26 11:30"}`, 0, ""},                         // past
		{`{"temperature": 12, "until": "2026-10-26 12:15"}`, 0, ""},                         // rounds down to now
		{`{"temperature": 40, "until": "2026-11-02 18:00"}`, 0, ""},                         // out of range
		{`{"temperature": 12}`, 0, ""},                                                      // missing until
		{`{"temperature": 12, "until": "02.11.2026 18:00"}`, 0, ""},                         // wrong format
		{`12`, 0, ""}, // no JSON object
	}
	for _, c := range cases {
		temp, until, err := parseVacationCommand(c.payload, 17, now)
		if c.wantUntil == "" {
			if err == nil {
				t.Errorf("%s: want error, got %.1f until %s", c.payload, temp, until.Format(untilLayout))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.payload, err)
			continue
		}
		if temp != c.wantTemp || until.Format(untilLayout) != c.wantUntil {
			t.Errorf("%s: got %.1f until %s", c.payload, temp, until.Format(untilLayout))
		}
	}
}

func TestDecodeVacationState(t *testing.T) {
	// ThermostatState in vacation mode: bits 0x1A (vacation), valve 0, setpoint 12.0, until 2026-11-02 18:00
	until := []byte{0x02 | 0x05<<5, 0x80 | 26, 36} // month 11 = 0b1011: bits 1-3 = 101, bit 0 = 1
	d := decodeThermostatStatus(0x60, append([]byte{0x1A, 0x00, 0x18}, until...), true)
	if d.Mode != "vacation" || d.Temperature != 12.0 || d.Until != "2026-11-02 18:00" {
		t.Errorf("0x60 vacation: mode %s temp %.1f until %q", d.Mode, d.Temperature, d.Until)
	}
	if d.CurrentTemperature != nil {
		t.Errorf("0x60 vacation: until bytes read as actual temperature %.1f", *d.CurrentTemperature)
	}
	// Same in auto mode: bytes 3-4 are the actual temperature, no until
	d = decodeThermostatStatus(0x60, []byte{0x18, 0x00, 0x2A, 0x00, 0xCD}, true)
	if d.Until != "" || d.CurrentTemperature == nil || *d.CurrentTemperature != 20.5 {
		t.Errorf("0x60 auto: until %q cur %v", d.Until, d.CurrentTemperature)
	}
	// SetTemperature 0x40 as sent by the bridge
	tm := time.Date(2026, 11, 2, 18, 0, 0, 0, time.Local)
	d = decodeSetTemperature(append([]byte{byte(12*2) | 0x80}, encodeUntil(tm)...))
	if d.Mode != "vacation" || d.Temperature != 12.0 || d.Until != "2026-11-02 18:00" {
		t.Errorf("0x40 vacation: mode %s temp %.1f until %q", d.Mode, d.Temperature, d.Until)
	}
}

func TestVacationNeedsTemperatureWhenEcoUnknown(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	if _, _, err := parseVacationCommand(`{"until": "2026-11-02 18:00"}`, 0, now); err == nil {
		t.Fatal("accepted vacation without temperature and unknown eco temperature")
	}
	if temp, _, err := parseVacationCommand(`{"temperature": 12, "until": "2026-11-02 18:00"}`, 0, now); err != nil || temp != 12 {
		t.Fatalf("temp %.1f err %v", temp, err)
	}
}

func TestVacationReportsUnknownActualTemperature(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	// Auto with a measurement of 16.5 °C
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x18, 0x50, 0x28, 0x00, 165}, true))
	if state := fc.get("max/" + dev + "/state"); !strings.Contains(state, `"current_temperature":16.5`) {
		t.Fatalf("auto: actual temperature missing: %s", state)
	}
	// Vacation: no measurement in the status, the old one must not stay
	until := []byte{0x02 | 0x05<<5, 0x80 | 26, 36}
	updateDeviceState(dev, decodeThermostatStatus(0x60, append([]byte{0x1A, 0x50, 0x28}, until...), true))
	if state := fc.get("max/" + dev + "/state"); !strings.Contains(state, `"current_temperature":null`) {
		t.Fatalf("vacation: actual temperature not unknown: %s", state)
	}
	// Back in auto with a new measurement
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x18, 0x50, 0x28, 0x00, 201}, true))
	if state := fc.get("max/" + dev + "/state"); !strings.Contains(state, `"current_temperature":20.1`) {
		t.Fatalf("auto again: actual temperature missing: %s", state)
	}
}

func TestVacationAckReportsUnknownActualTemperature(t *testing.T) {
	setupAckCounterTest(t)
	fc := mqttClient.(*fakeMQTT)
	// Wall thermostat (no valve): 18.0 °C measured, setpoint 20 °C → heating
	updateDeviceState(ackTestDevice, decodeWallThermostatControl([]byte{40, 180}))
	if state := fc.get("max/" + ackTestDevice + "/state"); !strings.Contains(state, `"hvac_action":"heating"`) {
		t.Fatalf("before vacation: %s", state)
	}
	ackTracker.TrackVacation(ackTestDevice, 20, "2026-11-08 17:00")
	sendCommand(ackTestDevice, 0x40, []byte{0xA8, 0x02, 0x08, 0x22}, "Set vacation")
	receiveShortAck(ackTestDevice, ackTestGateway, sentCounter(t))
	state := fc.get("max/" + ackTestDevice + "/state")
	if !strings.Contains(state, `"current_temperature":null`) || !strings.Contains(state, `"hvac_action":"idle"`) {
		t.Fatalf("after vacation Ack: %s", state)
	}
}

func TestVacationKeepsFreshWallThermostatMeasurement(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	until := []byte{0x02 | 0x05<<5, 0x80 | 26, 36}
	updateDeviceState(dev, decodeThermostatStatus(0x70, append([]byte{0x1A, 0x00, 0x28}, until...), false))
	updateDeviceState(dev, decodeWallThermostatControl([]byte{40, 195}))
	if state := fc.get("max/" + dev + "/state"); !strings.Contains(state, `"current_temperature":19.5`) {
		t.Fatalf("measurement of 0x42 lost in vacation: %s", state)
	}
}

func TestVacationClearsRestoredActualTemperature(t *testing.T) {
	resetTestState()
	deviceDataFile = filepath.Join(t.TempDir(), "devices.json")
	t.Setenv("DEVICE_DATA_FILE", deviceDataFile)
	const dev = "0A1B2C"
	saved := `{"` + dev + `":{"state":{"temperature":20,"current_temperature":16.5,"mode":"vacation","hvac_mode":"off","until":"2026-11-08 17:00"}}}`
	if err := os.WriteFile(deviceDataFile, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	loadDeviceData()
	if st := stateCache[dev]; st == nil || st.Mode != "vacation" || st.CurrentTemperature != nil {
		t.Fatalf("state after restart: %+v", st)
	}
}

package main

import (
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

package main

import "testing"

// Payloads after the 11-byte header, layouts as in FHEM 10_MAX.pm
func TestDecodeValvePosition(t *testing.T) {
	cases := []struct {
		name       string
		pktType    int
		payload    []byte
		isRadiator bool
		wantValve  int // -1 = no valve position
		wantRF     bool
	}{
		// ThermostatState: bits(auto), valve 37 %, setpoint 21.0, actual 20.5
		{"0x60 valve 37", 0x60, []byte{0x18, 0x25, 0x2A, 0x00, 0xCD}, false, 37, false},
		{"0x60 valve 0", 0x60, []byte{0x18, 0x00, 0x2A, 0x00, 0xCD}, false, 0, false},
		{"0x60 valve 100 rferror", 0x60, []byte{0x58, 0x64, 0x2A, 0x00, 0xCD}, false, 100, true},
		{"0x60 invalid valve", 0x60, []byte{0x18, 0xC8, 0x2A}, false, -1, false},
		// Ack from radiator thermostat: 01 + same layout
		{"0x02 radiator", 0x02, []byte{0x01, 0x19, 0x0F, 0x2A}, true, 15, false},
		// Ack from wall thermostat: byte after bits is the display flag, not a valve
		{"0x02 wall", 0x02, []byte{0x01, 0x19, 0x00, 0x2A, 0x00, 0xCD}, false, -1, false},
		// WallThermostatState: never a valve
		{"0x70 wall", 0x70, []byte{0x19, 0x04, 0x2A, 0x00, 0xCD}, false, -1, false},
	}
	for _, c := range cases {
		d := decodeThermostatStatus(c.pktType, c.payload, c.isRadiator)
		if d == nil {
			t.Fatalf("%s: nil result", c.name)
		}
		if c.wantValve < 0 {
			if d.ValvePosition != nil {
				t.Errorf("%s: valve = %d, want none", c.name, *d.ValvePosition)
			}
		} else if d.ValvePosition == nil || *d.ValvePosition != c.wantValve {
			t.Errorf("%s: valve = %v, want %d", c.name, d.ValvePosition, c.wantValve)
		}
		if d.RFError == nil || *d.RFError != c.wantRF {
			t.Errorf("%s: rf_error = %v, want %v", c.name, d.RFError, c.wantRF)
		}
		if d.Temperature != 21.0 {
			t.Errorf("%s: setpoint = %.1f, want 21.0", c.name, d.Temperature)
		}
	}
}

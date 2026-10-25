package main

import "testing"

func TestGatewayAddrHex(t *testing.T) {
	old := config.GatewayID
	defer func() { config.GatewayID = old }()

	cases := map[string]string{
		"123456": "123456",
		"0a1b2c": "0A1B2C",
		"bad":    "123456", // invalid config falls back to default
	}
	for in, want := range cases {
		config.GatewayID = in
		if got := gatewayAddrHex(); got != want {
			t.Errorf("gatewayAddrHex(%q) = %q, want %q", in, got, want)
		}
	}
}

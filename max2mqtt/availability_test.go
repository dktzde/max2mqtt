package main

import (
	"strings"
	"testing"
)

func (f *fakeMQTT) IsConnected() bool { return true }

func TestDiscoveryHasBridgeAvailability(t *testing.T) {
	fc := resetTestState()
	sendDiscovery("0A1B2C")
	cfg := fc.get("homeassistant/climate/max_0A1B2C/config")
	if !strings.Contains(cfg, `"availability_topic":"max/bridge/status"`) {
		t.Fatalf("climate discovery without availability: %s", cfg)
	}
	if !strings.Contains(fc.get("homeassistant/number/max_0A1B2C_eco_temp/config"), `"availability_topic"`) {
		t.Fatal("config entity without availability")
	}

	setCULOnline(true)
	if got := fc.get(bridgeStatusTopic); got != "online" {
		t.Fatalf("status %q", got)
	}
	setCULOnline(false)
	if got := fc.get(bridgeStatusTopic); got != "offline" {
		t.Fatalf("status %q", got)
	}
}

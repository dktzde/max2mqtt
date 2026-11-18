package main

import (
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	displayRadiator = "0D1A01"
	displayWall     = "0D1A02"
	displayUnknown  = "0D1A03"
)

// resetDisplayTest gives a test the shared fake state and an empty Display Mode marker
func resetDisplayTest(t *testing.T) *fakeMQTT {
	t.Helper()
	fc := resetTestState()
	displayModeCleared = sync.Map{}
	t.Cleanup(func() { displayModeCleared = sync.Map{} })
	return fc
}

// published reports whether a topic was published and its last payload
func (f *fakeMQTT) published(topic string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.last[topic]
	return v, ok
}

func TestDisplayModeOfferedForWallThermostatsOnly(t *testing.T) {
	fc := resetDisplayTest(t)
	markDeviceType(displayRadiator, 0x60)
	markDeviceType(displayWall, 0x70)

	publishConfigEntities(displayRadiator)
	publishConfigEntities(displayWall)
	publishConfigEntities(displayUnknown)

	if v, ok := fc.published(displayModeTopic(displayWall)); !ok || v == "" {
		t.Fatalf("wall thermostat: display mode discovery %q, published %v", v, ok)
	}
	if v, ok := fc.published(displayModeTopic(displayRadiator)); !ok || v != "" {
		t.Fatalf("radiator thermostat: want the retained discovery cleared, got %q, published %v", v, ok)
	}
	if v, ok := fc.published(displayModeTopic(displayUnknown)); ok {
		t.Fatalf("device of unknown type: want no display mode publish, got %q", v)
	}
}

func TestDisplayModeClearedOncePerRun(t *testing.T) {
	fc := resetDisplayTest(t)
	markDeviceType(displayRadiator, 0x60)
	publishConfigEntities(displayRadiator)

	fc.mu.Lock()
	delete(fc.last, displayModeTopic(displayRadiator))
	fc.mu.Unlock()
	publishConfigEntities(displayRadiator)

	if v, ok := fc.published(displayModeTopic(displayRadiator)); ok {
		t.Fatalf("cleared again on the next telegram: %q", v)
	}
}

func TestDisplayModeCommandIgnoredForRadiatorThermostat(t *testing.T) {
	resetDisplayTest(t)
	markDeviceType(displayRadiator, 0x60)

	handleMQTTDisplayMode(nil, fakeMessage{topic: "max/" + displayRadiator + "/display_mode/set", payload: "Actual"})

	if got := displayModeOf(displayRadiator); got == "Actual" {
		t.Fatal("display mode applied to a radiator thermostat")
	}
	select {
	case line := <-serialWrite:
		t.Fatalf("telegram sent to a radiator thermostat: %q", line)
	default:
	}
}

// failingMQTT is a fake MQTT client whose publishes are never confirmed
type failingMQTT struct{ *fakeMQTT }

type failedToken struct{ mqtt.Token }

func (failedToken) WaitTimeout(time.Duration) bool { return false }
func (failedToken) Error() error                   { return nil }

func (f failingMQTT) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	f.fakeMQTT.Publish(topic, qos, retained, payload)
	return failedToken{}
}

func TestDisplayModeClearRetriedAfterFailure(t *testing.T) {
	fc := resetDisplayTest(t)
	mqttClient = failingMQTT{fc}
	markDeviceType(displayRadiator, 0x60)

	publishConfigEntities(displayRadiator)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, marked := displayModeCleared.Load(displayRadiator); !marked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed clear still marked as done, it would never be tried again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

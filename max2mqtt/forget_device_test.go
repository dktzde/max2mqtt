package main

import (
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// forgetFakeMQTT delivers the recorded payloads as retained messages on Subscribe
type forgetFakeMQTT struct {
	*fakeMQTT
	unsubscribed []string
}

type retainedMessage struct {
	mqtt.Message
	topic, payload string
	retained       bool
}

func (m retainedMessage) Topic() string   { return m.topic }
func (m retainedMessage) Payload() []byte { return []byte(m.payload) }
func (m retainedMessage) Retained() bool  { return m.retained }

func (f *forgetFakeMQTT) Subscribe(filter string, _ byte, cb mqtt.MessageHandler) mqtt.Token {
	prefix := strings.TrimSuffix(filter, "#")
	f.mu.Lock()
	var msgs []retainedMessage
	for t, p := range f.last {
		if strings.HasPrefix(t, prefix) && p != "" {
			msgs = append(msgs, retainedMessage{topic: t, payload: p, retained: true})
		}
	}
	f.mu.Unlock()
	for _, m := range msgs {
		cb(f, m)
	}
	return fakeToken{}
}

func (f *forgetFakeMQTT) Unsubscribe(topics ...string) mqtt.Token {
	f.unsubscribed = append(f.unsubscribed, topics...)
	return fakeToken{}
}

const forgetDev, otherDev = "0A1B2C", "0A1B2D"

func setupForgetTest(t *testing.T) *forgetFakeMQTT {
	t.Helper()
	fc := &forgetFakeMQTT{fakeMQTT: resetTestState()}
	oldClient, oldWait, oldFile, oldSaved := mqttClient, forgetCollectWait, deviceDataFile, lastSaved
	mqttClient = fc
	forgetCollectWait = 0
	deviceDataFile, lastSaved = t.TempDir()+"/devices.json", nil
	recordNotifications(t, time.Hour)
	resetKnownDevices(t)
	t.Cleanup(func() {
		mqttClient, forgetCollectWait, deviceDataFile, lastSaved = oldClient, oldWait, oldFile, oldSaved
	})
	return fc
}

func TestForgetTopicMatches(t *testing.T) {
	yes := []string{
		"homeassistant/climate/0A1B2C/config",
		"homeassistant/binary_sensor/0A1B2C_battery/config",
		"homeassistant/button/max_0A1B2C_factory_reset/config",
		"homeassistant/text/max_0A1B2C_week_profile_monday/config",
	}
	no := []string{
		"homeassistant/climate/0A1B2D/config",
		"homeassistant/sensor/max_bridge_credits/config",
		"homeassistant/climate/10A1B2C/config",
		"max/0A1B2C/state",
		"homeassistant/sensor/0A1B2C/room_humidity/config",
	}
	for _, s := range yes {
		if !forgetTopicMatches(s, forgetDev) {
			t.Errorf("%s should match", s)
		}
	}
	for _, s := range no {
		if forgetTopicMatches(s, forgetDev) {
			t.Errorf("%s should not match", s)
		}
	}
}

func TestForgetDeviceClearsCachesAndTopics(t *testing.T) {
	fc := setupForgetTest(t)
	for _, id := range []string{forgetDev, otherDev} {
		stateMutex.Lock()
		stateCache[id] = &MaxDeviceData{Temperature: 20, Mode: "auto"}
		stateMutex.Unlock()
		getDeviceConfig(id)
		markPaired(id)
		fc.Publish("homeassistant/climate/"+id+"/config", 0, true, "{}")
		fc.Publish("homeassistant/button/max_"+id+"_factory_reset/config", 0, true, "{}")
		fc.Publish("max/"+id+"/state", 0, true, "{}")
	}

	forgetDevice(forgetDev)

	if isKnownDevice(forgetDev) {
		t.Fatal("device still known")
	}
	if !isKnownDevice(otherDev) {
		t.Fatal("other device forgotten too")
	}
	for _, topic := range []string{"homeassistant/climate/" + forgetDev + "/config", "homeassistant/button/max_" + forgetDev + "_factory_reset/config", "max/" + forgetDev + "/state"} {
		if fc.get(topic) != "" {
			t.Errorf("%s not cleared", topic)
		}
	}
	if fc.get("homeassistant/climate/"+otherDev+"/config") == "" || fc.get("max/"+otherDev+"/state") == "" {
		t.Fatal("topics of the other device cleared")
	}
	if len(fc.unsubscribed) != 1 || fc.unsubscribed[0] != forgetDiscoveryFilter {
		t.Fatalf("temporary subscription not ended: %v", fc.unsubscribed)
	}
	data := snapshotDeviceData()
	if _, ok := data[forgetDev]; ok {
		t.Fatal("device still in the saved data")
	}
}

// A late rollback of a pending config change must not create the device again
func TestForgetDropsPendingChanges(t *testing.T) {
	setupForgetTest(t)
	initAckTracker()
	ackTracker.TrackSetpoint(forgetDev, 21, "manual", "heat")
	ackTracker.Forget(forgetDev)
	ackTracker.mutex.Lock()
	_, pendingSet := ackTracker.pendingSetTemp[forgetDev]
	ackTracker.mutex.Unlock()
	if pendingSet {
		t.Fatal("pending setpoint kept")
	}
}

func TestRetainedForgetCommandIsIgnored(t *testing.T) {
	setupForgetTest(t)
	stateMutex.Lock()
	stateCache[forgetDev] = &MaxDeviceData{}
	stateMutex.Unlock()
	handleMQTTForget(nil, retainedMessage{topic: "max/" + forgetDev + "/forget/set", retained: true})
	handleMQTTForget(nil, retainedMessage{topic: "max/xyz/forget/set"})
	time.Sleep(50 * time.Millisecond)
	if !isKnownDevice(forgetDev) {
		t.Fatal("retained forget command was executed")
	}
}

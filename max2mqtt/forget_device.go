package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// "Forget device": the bridge removes a device from all its caches and from devices.json and clears
// the device's retained MQTT topics (Home Assistant discovery, state, attributes), so its entities
// disappear from Home Assistant and do not come back after a restart. Asked for on 2026-11-18:
// garbled CUL lines had created phantom devices from fragments of real addresses, and the bridge
// published them again on every start. Nothing is sent to the device; a real device that still
// talks to the gateway (or is paired again) is created again. To also reset the device itself, use
// Factory Reset first.
//
// MQTT: max/<id>/forget/set (any payload; the button "Forget Device" in Home Assistant).

var deviceIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

// forgetCollectWait is how long the bridge collects the retained topics of the device
var forgetCollectWait = 2 * time.Second

func handleMQTTForget(client mqtt.Client, msg mqtt.Message) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok || !deviceIDPattern.MatchString(deviceID) || deviceID == "000000" || deviceID == gatewayAddrHex() {
		slog.Error("Forget device: invalid device address", "topic", msg.Topic())
		return
	}
	if msg.Retained() {
		// a retained forget command would forget the device again on every start
		slog.Warn("Forget device: ignoring a retained command; publish it without retain", "device", deviceID)
		return
	}
	go forgetDevice(strings.ToUpper(deviceID))
}

// forgetMu runs one forget at a time (they share the temporary discovery subscription)
var forgetMu sync.Mutex

// forgetDevice removes the device from the bridge and clears its retained MQTT topics
func forgetDevice(deviceID string) {
	forgetMu.Lock()
	defer forgetMu.Unlock()
	slog.Warn("Forgetting device", "device", deviceID)
	forgetDeviceCaches(deviceID)
	saveDeviceData()
	n := clearRetainedTopics(deviceID)
	haDismiss(commandUnackedID(deviceID))
	slog.Warn("Device forgotten", "device", deviceID, "cleared_topics", n)
}

// forgetDeviceCaches removes every trace of the device from the bridge's memory
func forgetDeviceCaches(deviceID string) {
	if ackTracker != nil {
		ackTracker.Forget(deviceID)
	}
	stateMutex.Lock()
	delete(stateCache, deviceID)
	stateMutex.Unlock()
	configMutex.Lock()
	delete(deviceConfigCache, deviceID)
	configMutex.Unlock()
	valveMutex.Lock()
	delete(valveConfigCache, deviceID)
	valveMutex.Unlock()
	programmeMu.Lock()
	delete(programmes, deviceID)
	programmeMu.Unlock()
	programmeStatusMu.Lock()
	delete(programmeStatus, deviceID)
	delete(programmeStatusGen, deviceID)
	programmeStatusMu.Unlock()
	noDayGroupsMu.Lock()
	delete(noDayGroups, deviceID)
	noDayGroupsMu.Unlock()
	shutterMu.Lock()
	delete(shutterDiscovered, deviceID)
	delete(shutterLastCnt, deviceID)
	delete(shutterLastTime, deviceID)
	shutterMu.Unlock()
	pairedMu.Lock()
	delete(pairedDevices, deviceID)
	delete(unknownLogged, deviceID)
	pairedMu.Unlock()
	commandStatusMu.Lock()
	delete(commandStatus, deviceID)
	commandStatusMu.Unlock()
	configStatusMu.Lock()
	delete(configStatus, deviceID)
	configStatusMu.Unlock()
	commandUnackedMu.Lock()
	delete(commandUnackedNotified, deviceID)
	commandUnackedMu.Unlock()
	lastSeenMu.Lock()
	delete(lastSeenDevice, deviceID)
	delete(lastWakeUpQueued, deviceID)
	lastSeenMu.Unlock()
}

// Forget drops pending setpoints and config changes of the device and stops their rollback
// timers, so a late rollback does not create the device again
func (a *AckTracker) Forget(deviceID string) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	for _, entry := range a.pending[deviceID] {
		if entry.Timer != nil {
			entry.Timer.Stop()
		}
	}
	delete(a.pending, deviceID)
	delete(a.pendingSetTemp, deviceID)
}

// forgetTopicMatches reports whether a retained Home Assistant discovery topic belongs to the device
// (object IDs <id>, <id>_battery, max_<id>_..., max_<id>_week_profile_monday)
func forgetTopicMatches(topic, deviceID string) bool {
	if !strings.HasPrefix(topic, "homeassistant/") {
		return false
	}
	for _, part := range strings.Split(topic, "/") {
		p := strings.TrimPrefix(part, "max_")
		if p == deviceID || strings.HasPrefix(p, deviceID+"_") {
			return true
		}
	}
	return false
}

// deviceStateTopics are the retained topics the bridge publishes under max/<id>/. They are cleared
// by name: subscribing to max/<id>/# would also deliver retained command topics to the bridge's own
// command handlers (e.g. factory_reset/set).
func deviceStateTopics(deviceID string) []string {
	topics := []string{
		fmt.Sprintf(TopicFormatState, deviceID),
		fmt.Sprintf(TopicFormatValve, deviceID),
		fmt.Sprintf(TopicFormatAttributes, deviceID),
		fmt.Sprintf("max/%s/config", deviceID),
		fmt.Sprintf("max/%s/programme", deviceID),
	}
	for _, d := range weekDays {
		topics = append(topics, fmt.Sprintf("max/%s/week_profile/%s", deviceID, d))
	}
	return topics
}

// clearRetainedTopics clears the device's state topics, then subscribes for a moment to collect its
// retained discovery topics and publishes an empty retained message to each; returns how many
// discovery topics it cleared
func clearRetainedTopics(deviceID string) int {
	if mqttClient == nil {
		return 0
	}
	for _, topic := range deviceStateTopics(deviceID) {
		mqttClient.Publish(topic, 0, true, "")
	}
	var mu sync.Mutex
	found := map[string]bool{}
	collect := func(_ mqtt.Client, m mqtt.Message) {
		if m.Retained() && len(m.Payload()) > 0 && forgetTopicMatches(m.Topic(), deviceID) {
			mu.Lock()
			found[m.Topic()] = true
			mu.Unlock()
		}
	}
	if t := mqttClient.Subscribe(forgetDiscoveryFilter, 0, collect); t.Wait() && t.Error() != nil {
		slog.Error("Forget device: subscribe failed", "device", deviceID, "error", t.Error())
		return 0
	}
	time.Sleep(forgetCollectWait)
	mqttClient.Unsubscribe(forgetDiscoveryFilter).Wait()
	mu.Lock()
	defer mu.Unlock()
	for topic := range found {
		mqttClient.Publish(topic, 0, true, "")
	}
	return len(found)
}

// forgetDiscoveryFilter: the bridge has no other subscription below homeassistant/
const forgetDiscoveryFilter = "homeassistant/#"

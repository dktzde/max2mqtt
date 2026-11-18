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
	deviceID = strings.ToUpper(deviceID)
	if !ok || !deviceIDPattern.MatchString(deviceID) || deviceID == "000000" || deviceID == strings.ToUpper(gatewayAddrHex()) {
		slog.Error("Forget device: invalid device address", "topic", msg.Topic())
		return
	}
	if msg.Retained() {
		// a retained forget command would forget the device again on every start
		slog.Warn("Forget device: ignoring a retained command; publish it without retain", "device", deviceID)
		return
	}
	go forgetDevice(deviceID)
}

// forgetMu runs one forget at a time (they share the temporary discovery subscription)
var forgetMu sync.Mutex

// forgetDevice removes the device from the bridge and clears its retained MQTT topics
func forgetDevice(deviceID string) {
	forgetMu.Lock()
	defer forgetMu.Unlock()
	slog.Warn("Forgetting device", "device", deviceID)
	forgetDeviceCaches(deviceID)
	if err := saveDeviceDataErr(); err != nil {
		// the device would come back on the next start: keep its MQTT topics so the button can be pressed again
		slog.Error("Forget device: cannot save devices.json, press Forget Device again", "device", deviceID, "error", err)
		return
	}
	n, complete := clearRetainedTopics(deviceID)
	haDismiss(commandUnackedID(deviceID))
	if !complete {
		slog.Error("Forget device: not all retained topics could be cleared, press Forget Device again", "device", deviceID, "cleared_topics", n)
		return
	}
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
	// programmeStatusGen is kept: a transfer still queued must not match a later one
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

// forgetTopicMatches reports whether a retained Home Assistant discovery topic belongs to the device.
// The bridge publishes homeassistant/<component>/<object id>/config; only the object ID is compared
// (<id>, <id>_battery, max_<id>_..., max_<id>_week_profile_monday), so topics of other integrations
// with a node ID level are not touched.
func forgetTopicMatches(topic, deviceID string) bool {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "homeassistant" || parts[3] != "config" {
		return false
	}
	p := strings.TrimPrefix(parts[2], "max_")
	return p == deviceID || strings.HasPrefix(p, deviceID+"_")
}

// forgetPublishTimeout bounds the wait for the broker to confirm a cleared topic
var forgetPublishTimeout = 5 * time.Second

// clearRetained publishes an empty retained message with QoS 1 and reports whether the broker confirmed it
func clearRetained(topic string) bool {
	t := mqttClient.Publish(topic, 1, true, "")
	if !t.WaitTimeout(forgetPublishTimeout) || t.Error() != nil {
		slog.Error("Forget device: clearing a topic failed", "topic", topic, "error", t.Error())
		return false
	}
	return true
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
// retained discovery topics and publishes an empty retained message to each. The scan is best
// effort: the broker sends retained messages right after the subscription, and a topic it delivers
// after forgetCollectWait is missed (press the button again). Returns how many discovery topics the
// broker confirmed as cleared and whether every clear succeeded.
func clearRetainedTopics(deviceID string) (int, bool) {
	if mqttClient == nil {
		return 0, false
	}
	complete := true
	for _, topic := range deviceStateTopics(deviceID) {
		if !clearRetained(topic) {
			complete = false
		}
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
	t := mqttClient.Subscribe(forgetDiscoveryFilter, 0, collect)
	if t.Wait() && t.Error() != nil {
		slog.Error("Forget device: subscribe failed", "device", deviceID, "error", t.Error())
		return 0, false
	}
	if st, ok := t.(*mqtt.SubscribeToken); ok {
		for _, qos := range st.Result() {
			if qos == 0x80 {
				slog.Error("Forget device: the broker denied the subscription", "device", deviceID, "filter", forgetDiscoveryFilter)
				mqttClient.Unsubscribe(forgetDiscoveryFilter).Wait()
				return 0, false
			}
		}
	}
	time.Sleep(forgetCollectWait)
	mqttClient.Unsubscribe(forgetDiscoveryFilter).Wait()
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for topic := range found {
		if clearRetained(topic) {
			n++
		} else {
			complete = false
		}
	}
	return n, complete
}

// forgetDiscoveryFilter: the bridge has no other subscription below homeassistant/
const forgetDiscoveryFilter = "homeassistant/#"

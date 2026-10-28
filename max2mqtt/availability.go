package main

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// Bridge availability: "online" while the bridge is connected to MQTT and to the CUL,
// "offline" otherwise (MQTT last will, lost CUL connection, shutdown). All discovered
// entities use it, so Home Assistant shows them as unavailable instead of keeping old values.
const (
	bridgeStatusTopic = "max/bridge/status"
	payloadOnline     = "online"
	payloadOffline    = "offline"
)

var (
	culOnlineMu sync.Mutex
	culOnline   bool
)

// publishDiscovery publishes a retained MQTT discovery config with the bridge availability
func publishDiscovery(topic string, payload map[string]interface{}) {
	payload["availability_topic"] = bridgeStatusTopic
	payload["payload_available"] = payloadOnline
	payload["payload_not_available"] = payloadOffline
	bytes, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal discovery", "topic", topic, "error", err)
		return
	}
	mqttClient.Publish(topic, 0, true, bytes)
}

// setCULOnline records the CUL connection state and publishes the bridge status
func setCULOnline(online bool) {
	culOnlineMu.Lock()
	culOnline = online
	culOnlineMu.Unlock()
	publishBridgeStatus()
}

// publishBridgeStatus publishes online only while the CUL is connected
func publishBridgeStatus() {
	if mqttClient == nil || !mqttClient.IsConnected() {
		return
	}
	culOnlineMu.Lock()
	status := payloadOffline
	if culOnline {
		status = payloadOnline
	}
	culOnlineMu.Unlock()
	slog.Info("Bridge status", "status", status)
	mqttClient.Publish(bridgeStatusTopic, 1, true, status).Wait()
}

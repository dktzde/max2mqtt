package main

import (
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Test doubles for the MQTT client: record the last payload per topic.

type fakeToken struct{ mqtt.Token }

func (fakeToken) Wait() bool   { return true }
func (fakeToken) Error() error { return nil }

type fakeMQTT struct {
	mqtt.Client
	mu   sync.Mutex
	last map[string]string
}

func (f *fakeMQTT) Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch v := payload.(type) {
	case []byte:
		f.last[topic] = string(v)
	case string:
		f.last[topic] = v
	}
	return fakeToken{}
}

func (f *fakeMQTT) get(topic string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last[topic]
}

type fakeMessage struct {
	mqtt.Message
	topic, payload string
}

func (m fakeMessage) Topic() string   { return m.topic }
func (m fakeMessage) Payload() []byte { return []byte(m.payload) }

// resetTestState gives each test an empty cache, a fake MQTT client and a serial channel
func resetTestState() *fakeMQTT {
	fc := &fakeMQTT{last: map[string]string{}}
	mqttClient = fc
	serialWrite = make(chan string, 100)
	stateMutex.Lock()
	stateCache = map[string]*MaxDeviceData{}
	stateMutex.Unlock()
	configMutex.Lock()
	deviceConfigCache = map[string]*DeviceConfig{}
	configMutex.Unlock()
	valveMutex.Lock()
	valveConfigCache = map[string]*ValveConfig{}
	valveMutex.Unlock()
	programmeMu.Lock()
	programmes = map[string]Programme{}
	programmeMu.Unlock()
	txMgr = nil
	verificationMgr = nil
	ackTracker = nil
	return fc
}

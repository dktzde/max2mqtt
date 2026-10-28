package main

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Duty cycle sensors on the bridge device, from the CUL credit report ("X"):
// - TX Credits: remaining credits (10 ms of airtime each, culfw refills 1 per second up to 900)
// - Duty Cycle: used part of the CUL's credit budget in %, like the MAX! Cube's duty cycle value
const (
	creditsStateTopic = "max/bridge/credits"
	creditPollPeriod  = time.Minute
)

type creditState struct {
	Credits   int `json:"credits"`
	DutyCycle int `json:"duty_cycle"`
	CULQueue  int `json:"cul_queue"`
}

var (
	creditStateMu   sync.Mutex
	lastCreditState *creditState
)

// dutyCycleFromCredits returns the used part of the credit budget in percent
func dutyCycleFromCredits(credits int) int {
	if credits >= culCreditMax {
		return 0
	}
	if credits <= 0 {
		return 100
	}
	return (culCreditMax - credits) * 100 / culCreditMax
}

// publishCredits publishes the credit report when it changed
func publishCredits(credits, queueLen int) {
	st := &creditState{Credits: credits, DutyCycle: dutyCycleFromCredits(credits), CULQueue: queueLen}
	creditStateMu.Lock()
	if lastCreditState != nil && *lastCreditState == *st {
		creditStateMu.Unlock()
		return
	}
	lastCreditState = st
	creditStateMu.Unlock()
	if mqttClient == nil {
		return
	}
	bytes, _ := json.Marshal(st)
	mqttClient.Publish(creditsStateTopic, 0, true, bytes)
}

// startCreditPolling queries the credits regularly, so the sensors stay current without TX
func startCreditPolling() {
	time.Sleep(15 * time.Second)
	for {
		select {
		case serialWrite <- "X":
		default:
			slog.Debug("Credit poll skipped (serial queue full)")
		}
		time.Sleep(creditPollPeriod)
	}
}

func publishDutyCycleDiscovery(device map[string]string) {
	sensors := []struct {
		topic   string
		payload map[string]interface{}
	}{
		{"homeassistant/sensor/max_bridge_duty_cycle/config", map[string]interface{}{
			"name":                "Duty Cycle",
			"unique_id":           "max_bridge_duty_cycle",
			"icon":                "mdi:radio-tower",
			"unit_of_measurement": "%",
			"state_class":         "measurement",
			"value_template":      "{{ value_json.duty_cycle }}",
		}},
		{"homeassistant/sensor/max_bridge_credits/config", map[string]interface{}{
			"name":            "TX Credits",
			"unique_id":       "max_bridge_credits",
			"icon":            "mdi:counter",
			"state_class":     "measurement",
			"entity_category": "diagnostic",
			"value_template":  "{{ value_json.credits }}",
		}},
	}
	for _, s := range sensors {
		s.payload["state_topic"] = creditsStateTopic
		s.payload["device"] = device
		publishDiscovery(s.topic, s.payload)
	}
}

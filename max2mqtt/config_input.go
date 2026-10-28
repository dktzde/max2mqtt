package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Full device configuration as JSON, for
//   - max/<id>/config/import: store the device's real values without sending anything
//     (e.g. taken over from a MAX! Cube or FHEM), so single values can be changed safely later
//   - max/<id>/config/set: store and send them to the device
//
// A group is only accepted complete, because the device message always carries all of it:
// temperatures (0x11): comfort_temp, eco_temp, max_temp, min_temp, measurement_offset,
// window_open_temp, window_open_duration; valve (0x12, radiator thermostats): boost_duration,
// boost_valve_position, decalc_day, decalc_hour, max_valve_setting, valve_offset.
type configInput struct {
	ComfortTemp        *float64 `json:"comfort_temp"`
	EcoTemp            *float64 `json:"eco_temp"`
	MaxTemp            *float64 `json:"max_temp"`
	MinTemp            *float64 `json:"min_temp"`
	MeasurementOffset  *float64 `json:"measurement_offset"`
	WindowOpenTemp     *float64 `json:"window_open_temp"`
	WindowOpenDuration *int     `json:"window_open_duration"`

	BoostDuration      *int    `json:"boost_duration"`
	BoostValvePosition *int    `json:"boost_valve_position"`
	DecalcDay          *string `json:"decalc_day"`
	DecalcHour         *int    `json:"decalc_hour"`
	MaxValveSetting    *int    `json:"max_valve_setting"`
	ValveOffset        *int    `json:"valve_offset"`
}

// parseConfigInput validates the payload; it returns the new values and which groups were given
func parseConfigInput(payload []byte, temps DeviceConfig, valve ValveConfig) (DeviceConfig, ValveConfig, bool, bool, error) {
	var in configInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return temps, valve, false, false, fmt.Errorf("invalid JSON: %w", err)
	}

	tempFields := []*float64{in.ComfortTemp, in.EcoTemp, in.MaxTemp, in.MinTemp, in.MeasurementOffset, in.WindowOpenTemp}
	tempCount := 0
	for _, f := range tempFields {
		if f != nil {
			tempCount++
		}
	}
	if in.WindowOpenDuration != nil {
		tempCount++
	}
	hasTemps := tempCount > 0
	if hasTemps && tempCount != 7 {
		return temps, valve, false, false, fmt.Errorf("temperature group incomplete (%d of 7 values)", tempCount)
	}
	if hasTemps {
		for name, v := range map[string]float64{"comfort_temp": *in.ComfortTemp, "eco_temp": *in.EcoTemp, "max_temp": *in.MaxTemp, "min_temp": *in.MinTemp, "window_open_temp": *in.WindowOpenTemp} {
			if v < 4.5 || v > 30.5 || v*2 != math.Trunc(v*2) {
				return temps, valve, false, false, fmt.Errorf("%s %.1f: use 4.5-30.5 in steps of 0.5", name, v)
			}
		}
		if o := *in.MeasurementOffset; o < -3.5 || o > 3.5 || o*2 != math.Trunc(o*2) {
			return temps, valve, false, false, fmt.Errorf("measurement_offset %.1f: use -3.5-3.5 in steps of 0.5", o)
		}
		if d := *in.WindowOpenDuration; d < 0 || d > 60 || d%5 != 0 {
			return temps, valve, false, false, fmt.Errorf("window_open_duration %d: use 0-60 minutes in steps of 5", d)
		}
		temps.ComfortTemp, temps.EcoTemp = *in.ComfortTemp, *in.EcoTemp
		temps.MaxTemp, temps.MinTemp = *in.MaxTemp, *in.MinTemp
		temps.MeasurementOffset, temps.WindowOpenTemp = *in.MeasurementOffset, *in.WindowOpenTemp
		temps.WindowOpenDuration = *in.WindowOpenDuration
		temps.TempsKnown = true
	}

	valveCount := 0
	for _, f := range []*int{in.BoostDuration, in.BoostValvePosition, in.DecalcHour, in.MaxValveSetting, in.ValveOffset} {
		if f != nil {
			valveCount++
		}
	}
	if in.DecalcDay != nil {
		valveCount++
	}
	hasValve := valveCount > 0
	if hasValve && valveCount != 6 {
		return temps, valve, false, false, fmt.Errorf("valve group incomplete (%d of 6 values)", valveCount)
	}
	if hasValve {
		if _, ok := boostDurationToNibble[*in.BoostDuration]; !ok {
			return temps, valve, false, false, fmt.Errorf("boost_duration %d: use 0,5,10,15,20,25,30,60", *in.BoostDuration)
		}
		day := normalizeDecalcDay(*in.DecalcDay)
		if _, ok := decalcDayToNibble[day]; !ok {
			return temps, valve, false, false, fmt.Errorf("decalc_day %q: use Sat..Fri", *in.DecalcDay)
		}
		for name, v := range map[string]int{"boost_valve_position": *in.BoostValvePosition, "max_valve_setting": *in.MaxValveSetting, "valve_offset": *in.ValveOffset} {
			if v < 0 || v > 100 {
				return temps, valve, false, false, fmt.Errorf("%s %d: use 0-100", name, v)
			}
		}
		if h := *in.DecalcHour; h < 0 || h > 23 {
			return temps, valve, false, false, fmt.Errorf("decalc_hour %d: use 0-23", h)
		}
		valve.BoostDuration = *in.BoostDuration
		valve.BoostValvePosition = *in.BoostValvePosition / 5 * 5
		valve.DecalcDay, valve.DecalcHour = day, *in.DecalcHour
		valve.MaxValveSetting, valve.ValveOffset = *in.MaxValveSetting, *in.ValveOffset
		valve.ValveKnown = true
	}
	if !hasTemps && !hasValve {
		return temps, valve, false, false, fmt.Errorf("no config values given")
	}
	return temps, valve, hasTemps, hasValve, nil
}

// normalizeDecalcDay accepts "sat", "Saturday", ... and returns "Sat"
func normalizeDecalcDay(day string) string {
	d := strings.TrimSpace(day)
	if len(d) >= 3 {
		d = strings.ToUpper(d[:1]) + strings.ToLower(d[1:3])
	}
	return d
}

func handleMQTTConfigImport(client mqtt.Client, msg mqtt.Message) {
	applyConfigInput(msg, false)
}

func handleMQTTConfigSet(client mqtt.Client, msg mqtt.Message) {
	applyConfigInput(msg, true)
}

func applyConfigInput(msg mqtt.Message, send bool) {
	deviceID, ok := topicDeviceID(msg.Topic())
	if !ok {
		return
	}
	deviceID = strings.ToUpper(deviceID)
	prev := snapshotDevice(deviceID)
	temps, valve, hasTemps, hasValve, err := parseConfigInput(msg.Payload(), prev.Temps, prev.Valve)
	if err != nil {
		slog.Error("Invalid config payload", "device", deviceID, "topic", msg.Topic(), "error", err)
		return
	}
	next := prev
	next.Temps, next.Valve = temps, valve

	if !send {
		applyConfigSnapshot(deviceID, next)
		publishDeviceConfig(deviceID)
		slog.Info("Imported device config (not sent)", "device", deviceID, "temperatures", hasTemps, "valve", hasValve)
		return
	}
	if hasTemps {
		ackTracker.TrackConfig(deviceID, 0x11, "config_temperatures", prev, next)
		sendConfigTemperatures(deviceID, &next.Temps)
	}
	if hasValve {
		if hasTemps {
			prev = next
		}
		ackTracker.TrackConfig(deviceID, 0x12, "config_valve", prev, next)
		sendConfigValve(deviceID, &next.Valve)
	}
}

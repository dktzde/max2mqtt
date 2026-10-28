package main

import (
	"encoding/json"
	"fmt"
)

// Climate attributes in the style of the MAX! Cube integration, so templates and automations
// written for it keep working: state_attr('climate.x', 'valve_position'), ... 'programme', ...
// Published retained on max/<id>/attributes (json_attributes_topic of the climate entity).
// Config values only appear while they are known (see config/import), the programme only when
// the bridge has sent or imported one.
const TopicFormatAttributes = "max/%s/attributes"

func buildAttributes(deviceID string) map[string]interface{} {
	attrs := map[string]interface{}{
		"device_rf_address": deviceID,
	}

	stateMutex.RLock()
	if st, ok := stateCache[deviceID]; ok && st.ValvePosition != nil {
		attrs["valve_position"] = *st.ValvePosition
	}
	stateMutex.RUnlock()

	cfg := getDeviceConfig(deviceID)
	configMutex.RLock()
	c := *cfg
	configMutex.RUnlock()
	if c.GroupID > 0 {
		attrs["room"] = c.GroupID
	}
	if c.TempsKnown {
		attrs["comfort_temp"] = c.ComfortTemp
		attrs["eco_temp"] = c.EcoTemp
		attrs["window_open_temp"] = c.WindowOpenTemp
		attrs["window_open_duration"] = c.WindowOpenDuration // minutes
		attrs["temp_offset"] = c.MeasurementOffset
	}
	if c.IsRadiator || !c.IsWall {
		v := getValveConfig(deviceID)
		valveMutex.RLock()
		vc := *v
		valveMutex.RUnlock()
		if vc.ValveKnown {
			attrs["boost_value"] = vc.BoostValvePosition
			attrs["boost_duration"] = vc.BoostDuration
			attrs["decalc_day"] = int(decalcDayToNibble[vc.DecalcDay]) // 0 = Saturday, as the Cube integration
			attrs["decalc_time"] = vc.DecalcHour
			attrs["max_valve"] = vc.MaxValveSetting
			attrs["valve_offset"] = vc.ValveOffset
		}
	}

	programmeMu.Lock()
	if prog := programmes[deviceID]; len(prog) > 0 {
		p := Programme{}
		for d, s := range prog {
			p[d] = append([]ProgrammeSlot(nil), s...)
		}
		attrs["programme"] = p
	}
	programmeMu.Unlock()
	return attrs
}

// publishAttributes publishes the climate attributes of a device, retained
func publishAttributes(deviceID string) {
	if mqttClient == nil {
		return
	}
	bytes, err := json.Marshal(buildAttributes(deviceID))
	if err != nil {
		return
	}
	mqttClient.Publish(fmt.Sprintf(TopicFormatAttributes, deviceID), 0, true, bytes)
}

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCubeStyleAttributes(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	markDeviceType(dev, 0x60)
	updateDeviceGroupID(dev, 3)
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 67, 26, 0, 140}, true))

	var attrs map[string]interface{}
	if err := json.Unmarshal([]byte(fc.get("max/"+dev+"/attributes")), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["valve_position"] != float64(67) || attrs["room"] != float64(3) || attrs["device_rf_address"] != dev {
		t.Fatalf("attributes %v", attrs)
	}
	if _, ok := attrs["eco_temp"]; ok {
		t.Fatal("unknown eco temperature published")
	}
	if !strings.Contains(fc.get("max/"+dev+"/state"), `"hvac_action":"heating"`) {
		t.Fatalf("valve 67 %% must be heating: %s", fc.get("max/"+dev+"/state"))
	}

	handleMQTTConfigImport(nil, fakeMessage{topic: "max/" + dev + "/config/import", payload: `{
		"comfort_temp": 16, "eco_temp": 12, "max_temp": 30.5, "min_temp": 4.5, "measurement_offset": 0,
		"window_open_temp": 7, "window_open_duration": 30,
		"boost_duration": 15, "boost_valve_position": 100, "decalc_day": "Sat", "decalc_hour": 9,
		"max_valve_setting": 100, "valve_offset": 0}`})
	handleMQTTProgrammeImport(nil, fakeMessage{topic: "max/" + dev + "/programme/import", payload: `{"monday":[{"temp":9,"until":"05:20"},{"temp":18,"until":"24:00"}]}`})
	attrs = nil
	_ = json.Unmarshal([]byte(fc.get("max/"+dev+"/attributes")), &attrs)
	if attrs["eco_temp"] != float64(12) || attrs["boost_duration"] != float64(15) || attrs["decalc_day"] != float64(0) {
		t.Fatalf("config attributes %v", attrs)
	}
	prog, _ := attrs["programme"].(map[string]interface{})
	if mon, _ := prog["monday"].([]interface{}); len(mon) != 2 {
		t.Fatalf("programme %v", attrs["programme"])
	}

	// Valve closed -> idle
	updateDeviceState(dev, decodeThermostatStatus(0x60, []byte{0x00, 0, 26, 0, 140}, true))
	if !strings.Contains(fc.get("max/"+dev+"/state"), `"hvac_action":"idle"`) {
		t.Fatalf("valve 0 %% must be idle: %s", fc.get("max/"+dev+"/state"))
	}
}

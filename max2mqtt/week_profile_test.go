package main

import (
	"strings"
	"testing"
)

func queuedTX() []string {
	var out []string
	for len(serialWrite) > 0 {
		out = append(out, <-serialWrite)
	}
	return out
}

func TestFHEMDaySyntax(t *testing.T) {
	slots, err := parseFHEMDay("9,05:20,18,07:00,14,17:40,17.5,19:55,9")
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 5 || slots[4].Until != "24:00" || slots[3].Temp != 17.5 {
		t.Fatalf("slots %+v", slots)
	}
	if got := formatFHEMDay(slots); got != "9,05:20,18,07:00,14,17:40,17.5,19:55,9" {
		t.Fatalf("format %q", got)
	}
	for _, bad := range []string{"17,06:00", "17,06:03,20", "17,08:00,20,07:00,17", "40", ""} {
		if _, err := parseFHEMDay(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestProgrammeSetSendsOnlyNeededParts(t *testing.T) {
	fc := resetTestState()
	const dev = "0A1B2C"
	// Cube service format, 5 points on Monday (MAX! day 2): one telegram only
	handleMQTTProgrammeSet(nil, fakeMessage{topic: "max/" + dev + "/programme/set", payload: `{"day":"monday","slots":[
		{"temp":9,"until":"05:20"},{"temp":18,"until":"07:00"},{"temp":14,"until":"17:40"},{"temp":17.5,"until":"19:55"},{"temp":9,"until":"24:00"}]}`})
	tx := queuedTX()
	if len(tx) != 1 {
		t.Fatalf("want 1 telegram, got %v", tx)
	}
	// header 11 bytes (22 hex) after "Zs", then day 02 and 9 °C until 05:20 = (18<<9)|64 = 0x2440
	if !strings.HasPrefix(tx[0][2+22:], "022440") {
		t.Fatalf("payload %s", tx[0])
	}
	if got := fc.get("max/" + dev + "/week_profile/monday"); got != "9,05:20,18,07:00,14,17:40,17.5,19:55,9" {
		t.Fatalf("day state %q", got)
	}
	if !strings.Contains(fc.get("max/"+dev+"/programme"), `"monday":[{"temp":9,"until":"05:20"}`) {
		t.Fatalf("programme %s", fc.get("max/"+dev+"/programme"))
	}

	// Same day again: unchanged, nothing sent
	handleMQTTWeekProfileDay(nil, fakeMessage{topic: "max/" + dev + "/week_profile/monday/set", payload: "9,05:20,18,07:00,14,17:40,17.5,19:55,9"})
	if tx := queuedTX(); len(tx) != 0 {
		t.Fatalf("unchanged day sent: %v", tx)
	}

	// 9 points: two telegrams, the second with day | 0x10
	handleMQTTWeekProfileDay(nil, fakeMessage{topic: "max/" + dev + "/week_profile/tuesday/set", payload: "10,01:00,11,02:00,12,03:00,13,04:00,14,05:00,15,06:00,16,07:00,17,08:00,18"})
	tx = queuedTX()
	if len(tx) != 2 || !strings.HasPrefix(tx[1][2+22:], "13") {
		t.Fatalf("want part 0 and part 1 for Tuesday: %v", tx)
	}

	// Import stores without sending
	handleMQTTProgrammeImport(nil, fakeMessage{topic: "max/" + dev + "/programme/import", payload: `{"sunday":[{"temp":12,"until":"24:00"}]}`})
	if tx := queuedTX(); len(tx) != 0 {
		t.Fatalf("import sent: %v", tx)
	}
	if got := fc.get("max/" + dev + "/week_profile/sunday"); got != "12" {
		t.Fatalf("imported day %q", got)
	}
}

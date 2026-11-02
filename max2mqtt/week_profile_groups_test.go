package main

import (
	"strings"
	"testing"
)

func resetDayGroups(t *testing.T) {
	t.Helper()
	noDayGroupsMu.Lock()
	noDayGroups = map[string]bool{}
	noDayGroupsMu.Unlock()
	t.Cleanup(func() {
		noDayGroupsMu.Lock()
		noDayGroups = map[string]bool{}
		noDayGroupsMu.Unlock()
	})
}

// payloadOf returns the ConfigWeekProfile payload (day byte onwards) of a queued "Zs" telegram
func payloadOf(tx string) string {
	return strings.ToUpper(tx[2+22:])
}

func TestWholeWeekGoesOutAsTwoDayGroups(t *testing.T) {
	fc := resetTestState()
	resetDayGroups(t)
	const dev = "0A1B2C"
	// The telegrams a thermostat sent to its linked partner on 2026-11-02 after Monday-Friday was
	// edited on the device: day 8 = Monday-Friday, day 7 = Saturday/Sunday
	week := `{"monday":[{"temp":15,"until":"05:40"},{"temp":19,"until":"19:00"},{"temp":13,"until":"24:00"}],
		"tuesday":[{"temp":15,"until":"05:40"},{"temp":19,"until":"19:00"},{"temp":13,"until":"24:00"}],
		"wednesday":[{"temp":15,"until":"05:40"},{"temp":19,"until":"19:00"},{"temp":13,"until":"24:00"}],
		"thursday":[{"temp":15,"until":"05:40"},{"temp":19,"until":"19:00"},{"temp":13,"until":"24:00"}],
		"friday":[{"temp":15,"until":"05:40"},{"temp":19,"until":"19:00"},{"temp":13,"until":"24:00"}],
		"saturday":[{"temp":16,"until":"05:30"},{"temp":19.5,"until":"19:00"},{"temp":14,"until":"24:00"}],
		"sunday":[{"temp":16,"until":"05:30"},{"temp":19.5,"until":"19:00"},{"temp":14,"until":"24:00"}]}`
	handleMQTTProgrammeSet(nil, fakeMessage{topic: "max/" + dev + "/programme/set", payload: week})
	tx := queuedTX()
	if len(tx) != 2 {
		t.Fatalf("want 2 telegrams (two day groups), got %v", tx)
	}
	if got := payloadOf(tx[0]); got != "083C444CE435204520452045204520" {
		t.Fatalf("Monday-Friday telegram %s", got)
	}
	if got := payloadOf(tx[1]); got != "0740424EE439204520452045204520" {
		t.Fatalf("Saturday/Sunday telegram %s", got)
	}
	// Each day is still stored and published on its own
	if got := fc.get("max/" + dev + "/week_profile/wednesday"); got != "15,05:40,19,19:00,13" {
		t.Fatalf("Wednesday %q", got)
	}
}

func TestDayGroupOnlyWhenAllDaysAreTheSame(t *testing.T) {
	resetTestState()
	resetDayGroups(t)
	const dev = "0A1B2C"
	same := `[{"temp":17,"until":"06:00"},{"temp":20,"until":"24:00"}]`
	other := `[{"temp":18,"until":"24:00"}]`
	// Friday differs: Monday-Thursday go out one by one, Saturday/Sunday as a group
	handleMQTTProgrammeSet(nil, fakeMessage{topic: "max/" + dev + "/programme/set", payload: `{"monday":` + same +
		`,"tuesday":` + same + `,"wednesday":` + same + `,"thursday":` + same + `,"friday":` + other +
		`,"saturday":` + same + `,"sunday":` + same + `}`})
	var days []string
	for _, c := range queuedTX() {
		days = append(days, payloadOf(c)[:2])
	}
	if got := strings.Join(days, ","); got != "07,02,03,04,05,06" {
		t.Fatalf("day bytes %s, want 07,02,03,04,05,06", got)
	}

	// Only Monday changes: a single day, no group
	handleMQTTWeekProfileDay(nil, fakeMessage{topic: "max/" + dev + "/week_profile/monday/set", payload: "16"})
	tx := queuedTX()
	if len(tx) != 1 || payloadOf(tx[0])[:2] != "02" {
		t.Fatalf("want Monday alone: %v", tx)
	}
}

func TestUnackedDayGroupFallsBackToSingleDays(t *testing.T) {
	resetTestState()
	recordNotifications(t, 0)
	resetDayGroups(t)
	const dev = "0A1B2C"
	slots := []ProgrammeSlot{{Temp: 16, Until: "07:00"}, {Temp: 14, Until: "24:00"}}
	weekend := map[int][]ProgrammeSlot{0: slots, 1: slots}
	setProgramme(dev, weekend, false)

	// The device did not acknowledge the Saturday/Sunday telegram
	weekProfileDaysUnacked(dev, "saturday-sunday", []int{0, 1}, true)
	setProgramme(dev, weekend, true)
	var days []string
	for _, c := range queuedTX() {
		days = append(days, payloadOf(c)[:2])
	}
	if got := strings.Join(days, ","); got != "00,01" {
		t.Fatalf("day bytes %s, want single days 00,01", got)
	}
}

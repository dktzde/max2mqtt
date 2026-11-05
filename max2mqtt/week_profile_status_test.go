package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func resetProgrammeStatus(t *testing.T) {
	t.Helper()
	programmeStatusMu.Lock()
	programmeStatus = map[string]map[string]DayStatus{}
	programmeStatusMu.Unlock()
	t.Cleanup(func() {
		programmeStatusMu.Lock()
		programmeStatus = map[string]map[string]DayStatus{}
		programmeStatusMu.Unlock()
	})
}

// statusAttr reads programme_status from the last published attributes of dev
func statusAttr(t *testing.T, fc *fakeMQTT, dev string) map[string]DayStatus {
	t.Helper()
	raw := fc.get("max/" + dev + "/attributes")
	var attrs struct {
		Status map[string]DayStatus `json:"programme_status"`
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
			t.Fatalf("attributes %q: %v", raw, err)
		}
	}
	return attrs.Status
}

// waitStatus waits until day of dev has the wanted state in programme_status
func waitStatus(t *testing.T, dev, day, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st := programmeStatusOf(dev); st != nil && st[day].State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: state %+v, want %s", day, programmeStatusOf(dev)[day], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestImportedProgrammeHasNoStatus(t *testing.T) {
	fc := resetTestState()
	resetProgrammeStatus(t)
	const dev = "0A1B2C"
	slots := []ProgrammeSlot{{Temp: 16, Until: "07:00"}, {Temp: 14, Until: "24:00"}}
	setProgramme(dev, map[int][]ProgrammeSlot{2: slots}, false)
	if st := statusAttr(t, fc, dev); st != nil {
		t.Fatalf("an import sends nothing, want no programme_status, got %+v", st)
	}
}

func TestSentDayIsPendingUntilAcknowledged(t *testing.T) {
	setBulkAck(t, 2*time.Second, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	resetProgrammeStatus(t)
	resetDayGroups(t)
	fc := mqttClient.(*fakeMQTT)
	credits.Store(900)
	start()
	const dev = "0A1B2C"
	slots := []ProgrammeSlot{{Temp: 16, Until: "07:00"}, {Temp: 14, Until: "24:00"}}

	setProgramme(dev, map[int][]ProgrammeSlot{2: slots}, true) // Monday
	if st := statusAttr(t, fc, dev); st["monday"].State != statusPending || st["monday"].At == "" {
		t.Fatalf("after sending: %+v, want monday pending with a time", st)
	}
	if _, ok := statusAttr(t, fc, dev)["tuesday"]; ok {
		t.Fatal("a day that was not sent must not get a state")
	}
	select {
	case c := <-sent:
		cnt := parseCnt(t, c)
		txMgr.AckReceived(dev, cnt)
	case <-time.After(time.Second):
		t.Fatal("Monday not sent")
	}
	waitStatus(t, dev, "monday", statusAcknowledged)
	if st := statusAttr(t, fc, dev); st["monday"].State != statusAcknowledged {
		t.Fatalf("published %+v, want monday acknowledged", st)
	}
}

func TestUnacknowledgedDayIsReported(t *testing.T) {
	setBulkAck(t, 50*time.Millisecond, 1)
	recordNotifications(t, time.Minute)
	credits, _, start := startTxTest(t, time.Minute)
	resetProgrammeStatus(t)
	resetDayGroups(t)
	fc := mqttClient.(*fakeMQTT)
	credits.Store(900)
	start()
	const dev = "0A1B2C"
	slots := []ProgrammeSlot{{Temp: 16, Until: "07:00"}, {Temp: 14, Until: "24:00"}}

	setProgramme(dev, map[int][]ProgrammeSlot{3: slots}, true) // Tuesday, never acknowledged
	waitStatus(t, dev, "tuesday", statusNotAcknowledged)
	if st := statusAttr(t, fc, dev); st["tuesday"].State != statusNotAcknowledged {
		t.Fatalf("published %+v, want tuesday not_acknowledged", st)
	}
}

// A day with more than 7 points goes out in two telegrams: it is acknowledged only after both
func TestTwoPartDayNeedsBothAcks(t *testing.T) {
	setBulkAck(t, 2*time.Second, 3)
	credits, sent, start := startTxTest(t, time.Minute)
	resetProgrammeStatus(t)
	resetDayGroups(t)
	credits.Store(900)
	start()
	const dev = "0A1B2C"
	var slots []ProgrammeSlot
	for i, u := range []string{"05:00", "06:00", "07:00", "08:00", "09:00", "10:00", "11:00", "24:00"} {
		slots = append(slots, ProgrammeSlot{Temp: 15 + float64(i%2), Until: u})
	}
	setProgramme(dev, map[int][]ProgrammeSlot{4: slots}, true) // Wednesday, 8 points

	for part := 0; part < 2; part++ {
		select {
		case c := <-sent:
			if part == 0 {
				txMgr.AckReceived(dev, parseCnt(t, c))
				time.Sleep(50 * time.Millisecond)
				if st := programmeStatusOf(dev)["wednesday"].State; st != statusPending {
					t.Fatalf("after part 0 only: %s, want pending", st)
				}
			} else {
				txMgr.AckReceived(dev, parseCnt(t, c))
			}
		case <-time.After(time.Second):
			t.Fatalf("part %d not sent", part)
		}
	}
	waitStatus(t, dev, "wednesday", statusAcknowledged)
}

// parseCnt reads the message counter (second byte) of a queued "Zs" telegram
func parseCnt(t *testing.T, tx string) int {
	t.Helper()
	cnt, err := strconv.ParseInt(tx[4:6], 16, 0)
	if err != nil {
		t.Fatalf("counter of %s: %v", tx, err)
	}
	return int(cnt)
}

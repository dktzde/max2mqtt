package main

import (
	"strings"
	"testing"
)

func TestDutyCycleFromCredits(t *testing.T) {
	for credits, want := range map[int]int{900: 0, 1000: 0, 450: 50, 0: 100, 810: 10} {
		if got := dutyCycleFromCredits(credits); got != want {
			t.Errorf("credits %d: duty %d, want %d", credits, got, want)
		}
	}
}

func TestCreditReportPublished(t *testing.T) {
	fc := resetTestState()
	creditStateMu.Lock()
	lastCreditState = nil
	creditStateMu.Unlock()
	initTransmissionManager()
	processSerialLine("00 450", make(chan string, 1))
	if got := fc.get(creditsStateTopic); !strings.Contains(got, `"credits":450`) || !strings.Contains(got, `"duty_cycle":50`) {
		t.Fatalf("credit state %q", got)
	}
}

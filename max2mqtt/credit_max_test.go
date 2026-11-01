package main

import (
	"strings"
	"testing"
)

func TestCreditMaxFromVersion(t *testing.T) {
	resetTestState()
	processSerialLine("V 1.26.08 a-culfw Build: 323 (2019-08-03_09-32-54) CUBe (F-Band: 868MHz)", make(chan string, 1))
	if got := creditMax(); got != aculfwCreditMax {
		t.Fatalf("a-culfw: credit max %d, want %d", got, aculfwCreditMax)
	}
	processSerialLine("V 1.67 CUL868", make(chan string, 1))
	if got := creditMax(); got != aculfwCreditMax {
		t.Fatalf("line without culfw changed the credit max to %d", got)
	}
	processSerialLine("V 1.67 culfw Build: 0 CUL868", make(chan string, 1))
	if got := creditMax(); got != culfwCreditMax {
		t.Fatalf("culfw: credit max %d, want %d", got, culfwCreditMax)
	}
}

func TestDutyCycleWithACulfw(t *testing.T) {
	fc := resetTestState()
	creditStateMu.Lock()
	lastCreditState = nil
	creditStateMu.Unlock()
	initTransmissionManager()
	processSerialLine("V 1.26.08 a-culfw Build: 323 CUBe", make(chan string, 1))
	processSerialLine("00 2700", make(chan string, 1))
	if got := fc.get(creditsStateTopic); !strings.Contains(got, `"credits":2700`) || !strings.Contains(got, `"duty_cycle":25`) {
		t.Fatalf("credit state %q", got)
	}
}

func TestCreditMaxLearnedFromReport(t *testing.T) {
	resetTestState()
	initTransmissionManager()
	processSerialLine("00 1200", make(chan string, 1))
	if got := creditMax(); got != aculfwCreditMax {
		t.Fatalf("credit max %d after report 1200, want %d", got, aculfwCreditMax)
	}
	learnCreditMax(5000)
	if got := creditMax(); got != 5000 {
		t.Fatalf("credit max %d after report 5000, want 5000", got)
	}
	learnCreditMax(100)
	if got := creditMax(); got != 5000 {
		t.Fatalf("lower report changed the credit max to %d", got)
	}
}

func TestBulkReserveWithACulfw(t *testing.T) {
	resetTestState()
	detectCreditMax("V 1.26.08 a-culfw Build: 323 CUBe")
	mgr := &TransmissionManager{MinCredits: 200, ReservePercent: 20}
	if got := mgr.minCredits(QueuedCommand{Bulk: true}); got != 920 {
		t.Fatalf("bulk min credits %d, want 920", got)
	}
	if got := mgr.minCredits(QueuedCommand{}); got != 200 {
		t.Fatalf("normal min credits %d, want 200", got)
	}
}

package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QueuedCommand represents a TX command waiting in the local queue
type QueuedCommand struct {
	Payload     string
	Description string
	DeviceID    string
	CreatedAt   time.Time
	// Bulk: long transfers (week profile). They wait up to bulkCommandTimeout and
	// let normal commands (setpoint, mode, config) go first.
	Bulk bool
}

// bulkCommandTimeout is how long a bulk command may wait for duty cycle credits
const bulkCommandTimeout = 60 * time.Minute

// creditRetryInterval is the pause between credit queries while a command waits for credits.
// The CUL refills about 1 credit per second (1 % of an hour), a TX with wakeup preamble costs about 100.
var creditRetryInterval = 15 * time.Second

// TransmissionManager handles the complexity of sending commands to MAX! devices.
// It manages the Regulatory Duty Cycle (via CUL Credits), ensuring we don't exceed
// the 1% airtime limit. Commands are dispatched when credits are available.
//
// Note: We do NOT wait for ACKs. The MAX! system is self-correcting—the next RX
// packet from a device will contain its actual state, automatically fixing any
// discrepancy if a TX was lost.
type TransmissionManager struct {
	// Duty Cycle State
	CurrentCredits int
	QueueLength    int // Current buffering level reported by CUL (Real-time hardware state)
	MinCredits     int
	MaxQueue       int // Configured limit for concurrent commands (Software throttle)
	Timeout        time.Duration

	mutex          sync.RWMutex
	queue          chan QueuedCommand
	bulkQueue      chan QueuedCommand
	creditResponse chan struct{} // Signal when credit response received
	stop           chan struct{} // closed to end the dispatcher (tests); nil = run forever

}

var txMgr *TransmissionManager

// creditResponseRegex matches credit response format: "yy xxx" (e.g., "00 900", "01 850")
var creditResponseRegex = regexp.MustCompile(`^\d+\s+\d+$`)

// whitespaceRegex is precompiled for performance in sscanf
var whitespaceRegex = regexp.MustCompile(`\s+`)

func initTransmissionManager() {
	// Parse Timeout
	timeout, err := time.ParseDuration(config.CommandTimeout)
	if err != nil {
		slog.Warn("Invalid CommandTimeout, defaulting to 1m", "value", config.CommandTimeout)
		timeout = 1 * time.Minute
	}

	txMgr = &TransmissionManager{
		CurrentCredits: 0, // Start pessimistic - will query before first TX
		QueueLength:    0,
		MinCredits:     config.DutyCycleMinCredits,
		MaxQueue:       config.MaxCulQueue,
		Timeout:        timeout,
		queue:          make(chan QueuedCommand, 100),
		bulkQueue:      make(chan QueuedCommand, 200),
		creditResponse: make(chan struct{}, 1),
	}

	fmt.Printf("time=\"%s\" level=info msg=\"TransmissionManager initialized. MinCredits: %d, MaxQueue: %d, Timeout: %s\"\n",
		time.Now().Format(time.RFC3339), config.DutyCycleMinCredits, config.MaxCulQueue, timeout)

	go txMgr.dispatcherLoop()
	initVerificationManager()
}

func (t *TransmissionManager) Start() {
	// Optional: Any explicit start logic
}

// UpdateCredits updates both credits and queue length from CUL response
func (t *TransmissionManager) UpdateCredits(credits, queueLen int) {
	t.mutex.Lock()
	t.CurrentCredits = credits
	t.QueueLength = queueLen
	t.mutex.Unlock()
	slog.Debug("TxMgr: Updated", "credits", credits, "queue", queueLen)
}

// SignalCreditResponse notifies the dispatcher that a credit report "yy xxx" has been received.
// This unblocks the dispatcher if it was waiting for a credit check.
func (t *TransmissionManager) SignalCreditResponse() {
	select {
	case t.creditResponse <- struct{}{}:
	default:
		// Channel already has a signal, don't block
	}
}

// Enqueue adds a command to the transmission queue.
// It is safe to call from multiple goroutines (e.g., MQTT handlers).
func (t *TransmissionManager) Enqueue(devicedID, payload, description string) {
	t.enqueue(QueuedCommand{
		Payload:     payload,
		Description: description,
		DeviceID:    devicedID,
		CreatedAt:   time.Now(),
	})
}

// EnqueueBulk adds a command of a long transfer (week profile), see QueuedCommand.Bulk.
func (t *TransmissionManager) EnqueueBulk(devicedID, payload, description string) {
	t.enqueue(QueuedCommand{
		Payload:     payload,
		Description: description,
		DeviceID:    devicedID,
		CreatedAt:   time.Now(),
		Bulk:        true,
	})
}

func (t *TransmissionManager) enqueue(cmd QueuedCommand) {
	devicedID, description := cmd.DeviceID, cmd.Description
	queue := t.queue
	if cmd.Bulk {
		queue = t.bulkQueue
	}

	select {

	case queue <- cmd:
		slog.Debug("TxMgr: Enqueued command", "device", devicedID, "desc", description)
	default:
		slog.Warn("TxMgr: Queue full! Dropping command", "device", devicedID)
	}
}

// maybeRestoreState restores state for non-broadcast devices.
// Returns early if deviceID is the broadcast address.
func (t *TransmissionManager) maybeRestoreState(deviceID string) {
	if deviceID == "000000" {
		return
	}
	t.restoreState(deviceID)
}

// drainChannel clears any pending signal from a channel without blocking.
func drainChannel(ch <-chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

// dispatcherLoop consumes queued commands and dispatches them.
// Normal commands go first; a bulk command waiting for credits lets them pass.
// Broadcasts (deviceID=000000) skip state restoration.
// We do NOT wait for ACKs—the next RX from the device will self-correct state if needed.
func (t *TransmissionManager) dispatcherLoop() {
	var bulk *QueuedCommand
	for {
		select {
		case cmd := <-t.queue:
			t.processCommand(cmd)
			continue
		case <-t.stop:
			return
		default:
		}
		if bulk == nil {
			select {
			case cmd := <-t.queue:
				t.processCommand(cmd)
				continue
			case cmd := <-t.bulkQueue:
				bulk = &cmd
			case <-t.stop:
				return
			}
		}
		if t.tryDispatch(*bulk) {
			bulk = nil
			continue
		}
		select {
		case cmd := <-t.queue:
			t.processCommand(cmd)
		case <-time.After(creditRetryInterval):
		}
	}
}

// processCommand sends a normal command, waiting for duty cycle credits until its timeout.
func (t *TransmissionManager) processCommand(cmd QueuedCommand) {
	for !t.tryDispatch(cmd) {
		select {
		case <-time.After(creditRetryInterval):
		case <-t.stop:
			return
		}
	}
}

// deadline returns the time until which the command may wait for credits
func (t *TransmissionManager) deadline(cmd QueuedCommand) time.Time {
	if cmd.Bulk {
		return cmd.CreatedAt.Add(bulkCommandTimeout)
	}
	return cmd.CreatedAt.Add(t.Timeout)
}

// tryDispatch sends the command if the CUL has credits and queue space.
// Returns true when the command is done (sent, or dropped after its timeout),
// false when it has to wait for credits.
func (t *TransmissionManager) tryDispatch(cmd QueuedCommand) bool {
	// 1. Check Timeout before even querying
	if time.Now().After(t.deadline(cmd)) {
		slog.Warn("Command timed out due to duty cycle limits. Restoring state.", "device", cmd.DeviceID, "desc", cmd.Description, "wait", time.Since(cmd.CreatedAt))
		t.maybeRestoreState(cmd.DeviceID)
		return true
	}

	// 2. Query credits and wait for response
	credits, queueLen, ok := t.queryCreditStatus()
	if !ok {
		// PairPong bypasses TxMgr; if credit query fails we still TX with a warning
		// so setpoint/mode changes are not silently dropped (HA would appear "broken").
		t.mutex.RLock()
		credits = t.CurrentCredits
		queueLen = t.QueueLength
		t.mutex.RUnlock()
		slog.Warn("TxMgr: Credit query failed; sending anyway", "device", cmd.DeviceID, "lastCredits", credits, "queue", queueLen)
	}

	if queueLen >= t.MaxQueue {
		slog.Warn("TxMgr: CUL queue too busy. Waiting.", "queue", queueLen, "limit", t.MaxQueue, "device", cmd.DeviceID, "desc", cmd.Description)
		return false
	}

	if ok && credits < t.MinCredits {
		slog.Warn("TxMgr: Insufficient credits. Waiting.", "credits", credits, "min", t.MinCredits, "device", cmd.DeviceID, "desc", cmd.Description, "until", t.deadline(cmd).Format(time.TimeOnly))
		return false
	}

	// Brief pause after credit query so TCP/serial bridges finish the X reply before Zs
	time.Sleep(50 * time.Millisecond)
	t.dispatchToSerial(cmd, credits, queueLen)
	return true
}

// queryCreditStatus sends a credit query and waits for the response.
// Returns (credits, queueLen, success).
func (t *TransmissionManager) queryCreditStatus() (credits int, queueLen int, ok bool) {
	slog.Debug("TxMgr: Querying credits before TX (X)")

	drainChannel(t.creditResponse)

	select {
	case serialWrite <- "X":
	default:
		slog.Warn("TxMgr: Could not send credit query (channel full)")
		return 0, 0, false
	}

	const queryTimeout = 5 * time.Second
	select {
	case <-t.creditResponse:
	case <-time.After(queryTimeout):
		slog.Warn("TxMgr: Credit query timeout", "timeout", queryTimeout)
		return 0, 0, false
	}

	t.mutex.RLock()
	credits = t.CurrentCredits
	queueLen = t.QueueLength
	t.mutex.RUnlock()

	slog.Debug("TxMgr: Credit query OK", "credits", credits, "cul_queue", queueLen)
	return credits, queueLen, true
}

// dispatchToSerial sends the command payload to the serial channel.
func (t *TransmissionManager) dispatchToSerial(cmd QueuedCommand, credits, queueLen int) {
	select {

	case serialWrite <- cmd.Payload:
		slog.Info("TxMgr: TX dispatched", "device", cmd.DeviceID, "desc", cmd.Description, "credits", credits, "queue", queueLen)
	default:
		slog.Error("TxMgr: Serial write channel full! Dropping command", "device", cmd.DeviceID)
	}
}

func (t *TransmissionManager) restoreState(deviceID string) {
	stateMutex.RLock()
	existing, ok := stateCache[deviceID]
	stateMutex.RUnlock()

	if !ok {
		slog.Warn("TxMgr: No state known to restore.", "device", deviceID)
		return
	}

	// Re-publish the existing clean state to MQTT
	// This overwrites any optimistic UI changes in HA
	slog.Info("TxMgr: Restoring state", "device", deviceID, "state", existing)
	publishState(deviceID, existing)
}

// ParseCreditResponse checks if a line matches the credit response format
// and returns (queueLen, credits, matched)
func ParseCreditResponse(text string) (queueLen int, credits int, matched bool) {
	text = strings.TrimSpace(text)
	if creditResponseRegex.MatchString(text) {
		var q, c int
		if _, err := sscanf(text, &q, &c); err == nil {
			return q, c, true
		}
	}
	// Some firmwares reply with a bare credit number after X
	if c, err := strconv.Atoi(text); err == nil && c >= 0 && c <= 9999 {
		return 0, c, true
	}
	return 0, 0, false
}

// sscanf is a helper to parse two integers from a space-separated string
func sscanf(text string, q *int, c *int) (int, error) {
	parts := whitespaceRegex.Split(text, -1)
	if len(parts) < 2 {
		return 0, nil
	}

	qVal, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	*q = qVal

	cVal, err := strconv.Atoi(parts[1])
	if err != nil {
		return 1, err
	}
	*c = cVal

	return 2, nil
}

// -----------------------------------------------------------------------------------
// Verification Manager Logic
// -----------------------------------------------------------------------------------

// VerificationEntry tracks a pending verification
type VerificationEntry struct {
	TargetTemp float64
	Timer      *time.Timer
	ResendFunc func()
}

// VerificationManager manages pending command verifications
type VerificationManager struct {
	mutex         sync.Mutex
	verifications map[string]*VerificationEntry // Key: DeviceID
	timeout       time.Duration
}

var verificationMgr *VerificationManager

// initVerificationManager initializes the global verification manager
func initVerificationManager() {
	timeoutDuration, err := time.ParseDuration(config.TxCheckPeriod)
	if err != nil {
		slog.Warn("Invalid TxCheckPeriod, defaulting to 1m", "value", config.TxCheckPeriod)
		timeoutDuration = 1 * time.Minute
	}

	verificationMgr = &VerificationManager{
		verifications: make(map[string]*VerificationEntry),
		timeout:       timeoutDuration,
	}
	slog.Info("VerificationManager initialized", "period", timeoutDuration)
}

// AddVerification starts tracking a verification for a device
// resendFunc is a closure that will be executed if verification fails
func (vm *VerificationManager) AddVerification(deviceID string, targetTemp float64, resendFunc func()) {
	vm.mutex.Lock()
	defer vm.mutex.Unlock()

	// Stop existing timer if any (last write wins)
	if entry, exists := vm.verifications[deviceID]; exists {
		entry.Timer.Stop()
	}

	slog.Info("Verification started", "device", deviceID, "target", targetTemp, "timeout", vm.timeout)

	// Create new entry with timer
	timer := time.AfterFunc(vm.timeout, func() {
		vm.handleTimeout(deviceID, targetTemp)
	})

	vm.verifications[deviceID] = &VerificationEntry{
		TargetTemp: targetTemp,
		Timer:      timer,
		ResendFunc: resendFunc,
	}
}

// CheckVerification checks if the received actualTemp matches the pending verification
func (vm *VerificationManager) CheckVerification(deviceID string, actualTemp float64) {
	vm.mutex.Lock()
	defer vm.mutex.Unlock()

	entry, exists := vm.verifications[deviceID]
	if !exists {
		return
	}

	if actualTemp == entry.TargetTemp {
		slog.Info("Verification successful", "device", deviceID, "temp", actualTemp)
		entry.Timer.Stop()
		delete(vm.verifications, deviceID)
	} else {
		// Mismatch - we just wait for timeout OR subsequent update
		slog.Debug("Verification mismatch. Waiting...", "device", deviceID, "wanted", entry.TargetTemp, "got", actualTemp)
	}
}

// handleTimeout is called when the verification timer expires
func (vm *VerificationManager) handleTimeout(deviceID string, targetTemp float64) {
	vm.mutex.Lock()
	// Check if entry still exists and matches expected temp (handle race conditions)
	entry, exists := vm.verifications[deviceID]
	if !exists || entry.TargetTemp != targetTemp {
		vm.mutex.Unlock()
		return
	}
	// Remove the failed verification
	delete(vm.verifications, deviceID)
	// Extract resend func to call outside lock
	resendFunc := entry.ResendFunc
	vm.mutex.Unlock()

	slog.Warn("Verification failed: Did not receive confirmation. Resending...", "device", deviceID, "expected", targetTemp)

	// Execute resend
	if resendFunc != nil {
		resendFunc()
	}
}

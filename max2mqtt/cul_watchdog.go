package main

import (
	"log/slog"
	"sync"
	"time"
)

// CUL watchdog: a CUL can stop answering while its serial port or TCP connection stays open
// (seen with a MAX! Cube flashed with a-culfw: no line at all for 11 minutes, credit queries
// timed out, commands went out without an Ack, until the TCP connection dropped by itself).
// The reader loop then never notices. The watchdog closes such a connection, so
// serialReaderLoop opens it again and sends the init commands.
//
// The CUL answers the credit poll (creditPollPeriod), so a live CUL sends a line at least once
// a minute. Single credit query timeouts happen on a live CUL too (busy radio), so they only
// count when the CUL has also been silent for a while.
const (
	culWatchdogPeriod = 10 * time.Second
	// culSilenceTimeout: no line for this long → reconnect (two missed credit polls)
	culSilenceTimeout = 2*creditPollPeriod + 30*time.Second
	// culSuspectSilence: a credit query timeout reconnects only after this much silence
	culSuspectSilence = creditPollPeriod + 15*time.Second
	// culReconnectWait: how long a command waits for the new connection after such a reconnect
	culReconnectWait = 20 * time.Second
)

// culLink tracks the open CUL connection and the time of the last line received from it
type culLink struct {
	mu     sync.Mutex
	conn   CulConn
	lastRX time.Time
	gen    uint64 // counts connections that finished their init
	ready  chan struct{}
	now    func() time.Time
}

var cul = newCulLink(time.Now)

func newCulLink(now func() time.Time) *culLink {
	return &culLink{ready: make(chan struct{}), now: now}
}

// connected records a CUL connection after its init commands
func (l *culLink) connected(conn CulConn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conn = conn
	l.lastRX = l.now()
	l.gen++
	close(l.ready)
	l.ready = make(chan struct{})
}

// disconnected forgets the connection (the reader loop closes it)
func (l *culLink) disconnected(conn CulConn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == conn {
		l.conn = nil
	}
}

// noteRX records that the CUL sent a line
func (l *culLink) noteRX() {
	l.mu.Lock()
	l.lastRX = l.now()
	l.mu.Unlock()
}

// silence returns how long the open connection has sent nothing (0 without a connection)
func (l *culLink) silence() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return 0
	}
	return l.now().Sub(l.lastRX)
}

// reconnectIfSilent closes the connection when it has been silent for at least limit.
// It returns the generation of the closed connection and true when it closed one.
func (l *culLink) reconnectIfSilent(limit time.Duration, reason string) (uint64, bool) {
	l.mu.Lock()
	if l.conn == nil {
		l.mu.Unlock()
		return 0, false
	}
	silent := l.now().Sub(l.lastRX)
	if silent < limit {
		l.mu.Unlock()
		return 0, false
	}
	conn, gen := l.conn, l.gen
	l.conn = nil
	l.mu.Unlock()

	slog.Warn("CUL not answering, closing the connection to reconnect", "reason", reason, "silent", silent.Round(time.Second))
	if err := conn.Close(); err != nil {
		slog.Debug("Closing the silent CUL connection", "error", err)
	}
	return gen, true
}

// pendingReconnect returns the generation of the last connection and true while there is no
// connection after an earlier one (closed by the watchdog or dropped), i.e. a reconnect is due
func (l *culLink) pendingReconnect() (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gen, l.conn == nil && l.gen > 0
}

// waitReconnected waits until a connection newer than gen has finished its init
func (l *culLink) waitReconnected(gen uint64, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		l.mu.Lock()
		if l.gen > gen && l.conn != nil {
			l.mu.Unlock()
			return true
		}
		ready := l.ready
		l.mu.Unlock()
		select {
		case <-ready:
		case <-deadline:
			return false
		}
	}
}

// startCULWatchdog reconnects a CUL that sent nothing for culSilenceTimeout
func startCULWatchdog() {
	ticker := time.NewTicker(culWatchdogPeriod)
	defer ticker.Stop()
	for range ticker.C {
		cul.reconnectIfSilent(culSilenceTimeout, "no data received")
	}
}

// reconnectAfterCreditTimeout reconnects a CUL that did not answer a credit query and has been
// silent for culSuspectSilence (or waits for a reconnect already under way), and waits for the
// new connection. It reports whether a new
// connection is ready, so the caller can query the credits again.
func reconnectAfterCreditTimeout() bool {
	gen, closed := cul.reconnectIfSilent(culSuspectSilence, "credit query timeout")
	if !closed {
		// The watchdog (or the reader loop) may have closed it just before: wait for that reconnect too
		if gen, closed = cul.pendingReconnect(); !closed {
			return false
		}
	}
	if !cul.waitReconnected(gen, culReconnectWait) {
		slog.Warn("CUL not back after reconnect", "waited", culReconnectWait)
		return false
	}
	slog.Info("CUL reconnected after credit query timeout")
	return true
}

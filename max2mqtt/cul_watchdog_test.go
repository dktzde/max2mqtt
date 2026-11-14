package main

import (
	"sync"
	"testing"
	"time"
)

// fakeCulConn is a CulConn that only records Close
type fakeCulConn struct {
	mu     sync.Mutex
	closed int
}

func (c *fakeCulConn) Read(p []byte) (int, error)  { return 0, nil }
func (c *fakeCulConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeCulConn) ResetInputBuffer() error     { return nil }
func (c *fakeCulConn) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}
func (c *fakeCulConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// testClock is a settable clock for culLink
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestLink() (*culLink, *testClock) {
	clk := &testClock{t: time.Date(2026, 11, 14, 6, 26, 48, 0, time.UTC)}
	return newCulLink(clk.now), clk
}

func TestCULWatchdogKeepsALiveConnection(t *testing.T) {
	l, clk := newTestLink()
	conn := &fakeCulConn{}
	l.connected(conn)

	// a line every minute (credit poll) keeps the connection
	for i := 0; i < 5; i++ {
		clk.advance(creditPollPeriod)
		l.noteRX()
		if _, closed := l.reconnectIfSilent(culSilenceTimeout, "test"); closed {
			t.Fatalf("closed a connection that answers every minute (round %d)", i)
		}
	}
	if conn.closeCount() != 0 {
		t.Fatalf("Close called %d times", conn.closeCount())
	}
}

func TestCULWatchdogClosesASilentConnection(t *testing.T) {
	l, clk := newTestLink()
	conn := &fakeCulConn{}
	l.connected(conn)

	clk.advance(culSilenceTimeout - time.Second)
	if _, closed := l.reconnectIfSilent(culSilenceTimeout, "test"); closed {
		t.Fatal("closed before culSilenceTimeout")
	}
	clk.advance(time.Second)
	if _, closed := l.reconnectIfSilent(culSilenceTimeout, "test"); !closed {
		t.Fatal("silent connection not closed")
	}
	if conn.closeCount() != 1 {
		t.Fatalf("Close called %d times, want 1", conn.closeCount())
	}
	// closed once only, also when the reader loop has not noticed yet
	clk.advance(culSilenceTimeout)
	if _, closed := l.reconnectIfSilent(culSilenceTimeout, "test"); closed {
		t.Fatal("closed the same connection twice")
	}
	if l.silence() != 0 {
		t.Fatal("silence without a connection should be 0")
	}
}

// A credit query timeout on a live CUL (seen many times while the radio is busy) must not reconnect
func TestCreditTimeoutOnLiveCULDoesNotReconnect(t *testing.T) {
	l, clk := newTestLink()
	conn := &fakeCulConn{}
	l.connected(conn)

	clk.advance(5 * time.Second)
	if _, closed := l.reconnectIfSilent(culSuspectSilence, "credit query timeout"); closed {
		t.Fatal("reconnected a CUL that sent a line 5 s ago")
	}
	if conn.closeCount() != 0 {
		t.Fatal("Close called")
	}
}

func TestWaitReconnectedReturnsWithTheNewConnection(t *testing.T) {
	l, clk := newTestLink()
	old := &fakeCulConn{}
	l.connected(old)
	clk.advance(culSuspectSilence)

	gen, closed := l.reconnectIfSilent(culSuspectSilence, "credit query timeout")
	if !closed {
		t.Fatal("not closed")
	}
	l.disconnected(old) // reader loop ends

	done := make(chan bool, 1)
	go func() { done <- l.waitReconnected(gen, 2*time.Second) }()
	time.Sleep(20 * time.Millisecond)
	l.connected(&fakeCulConn{})

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waitReconnected returned false after the new connection")
		}
	case <-time.After(time.Second):
		t.Fatal("waitReconnected did not return")
	}
}

func TestWaitReconnectedTimesOut(t *testing.T) {
	l, _ := newTestLink()
	l.connected(&fakeCulConn{})
	gen := l.gen
	if l.waitReconnected(gen, 50*time.Millisecond) {
		t.Fatal("no new connection, but waitReconnected returned true")
	}
}

// disconnected of an old connection must not forget a newer one
func TestDisconnectedKeepsANewerConnection(t *testing.T) {
	l, clk := newTestLink()
	old, newer := &fakeCulConn{}, &fakeCulConn{}
	l.connected(old)
	l.connected(newer)
	l.disconnected(old)
	clk.advance(time.Second)
	if l.silence() != time.Second {
		t.Fatalf("silence %v, want 1s (newer connection forgotten?)", l.silence())
	}
}

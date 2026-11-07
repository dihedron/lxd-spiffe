package lxdiid

import (
	"testing"
	"time"
)

// clock is a test clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(inflight, rate int) (*limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	return newLimiter(limitSettings{maxInflight: inflight, ratePerSec: rate}, c.now), c
}

// TestSEC09Inflight: attestations beyond max_inflight are refused at once,
// not queued; a released slot is free again.
func TestSEC09Inflight(t *testing.T) {
	l, _ := newTestLimiter(2, 100)
	r1, ok1 := l.acquire()
	_, ok2 := l.acquire()
	if !ok1 || !ok2 || l.inflight() != 2 {
		t.Fatalf("two slots: %v %v, inflight %d", ok1, ok2, l.inflight())
	}
	if _, ok := l.acquire(); ok {
		t.Fatal("a third attestation got a slot")
	}
	r1()
	if _, ok := l.acquire(); !ok {
		t.Error("a released slot was not free again")
	}
}

// TestSEC09TokenBucket: rate_per_sec tokens per second, a burst of as
// many; a refused attestation does not hold a slot.
func TestSEC09TokenBucket(t *testing.T) {
	l, c := newTestLimiter(100, 2)
	for i := range 2 {
		release, ok := l.acquire()
		if !ok {
			t.Fatalf("token %d refused", i)
		}
		release()
	}
	if _, ok := l.acquire(); ok {
		t.Fatal("a third token in the same second")
	}
	if l.inflight() != 0 {
		t.Errorf("a refused attestation holds a slot: inflight %d", l.inflight())
	}
	c.advance(500 * time.Millisecond)
	if _, ok := l.acquire(); !ok {
		t.Error("no token after half a second at 2/s")
	}
	c.advance(time.Hour)
	for i := range 3 {
		release, ok := l.acquire()
		if ok {
			release()
		}
		if ok != (i < 2) {
			t.Errorf("after an hour, token %d: %v (the burst is the rate)", i, ok)
		}
	}
}

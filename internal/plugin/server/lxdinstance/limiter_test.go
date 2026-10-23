package lxdinstance

import (
	"testing"
	"time"
)

// clock is a test clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(inflight, rate int, cooldown time.Duration) (*limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	return newLimiter(limitSettings{maxInflight: inflight, ratePerSec: rate, failureCooldown: cooldown}, c.now), c
}

// TestSEC09Inflight: attestations beyond max_inflight are refused at once,
// not queued; a released slot is free again.
func TestSEC09Inflight(t *testing.T) {
	l, _ := newTestLimiter(2, 100, time.Second)
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
	l, c := newTestLimiter(100, 2, time.Second)
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

// TestSEC09Cooldown: a claim that failed waits failure_cooldown, doubling
// with each failure up to 5 minutes; a success clears it.
func TestSEC09Cooldown(t *testing.T) {
	l, c := newTestLimiter(10, 10, 5*time.Second)
	key := "/c1"
	if l.cooling(key) {
		t.Fatal("a new claim is cooling")
	}
	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} {
		l.outcome(key, false)
		c.advance(want - time.Millisecond)
		if !l.cooling(key) {
			t.Fatalf("failure %d: free before %v", i+1, want)
		}
		c.advance(time.Millisecond)
		if l.cooling(key) {
			t.Fatalf("failure %d: still cooling after %v", i+1, want)
		}
	}
	for range 10 {
		l.outcome(key, false)
	}
	c.advance(maxCooldown)
	if l.cooling(key) {
		t.Error("cooling longer than the 5-minute cap")
	}
	l.outcome(key, false)
	l.outcome(key, true)
	if l.cooling(key) {
		t.Error("a success did not clear the cooldown")
	}
	if l.cooling("/other") {
		t.Error("another claim is cooling")
	}
}

// TestSEC09CooldownPruned: claims idle past their cooldown and its cap are
// forgotten, so random claims cannot grow the table without bound.
func TestSEC09CooldownPruned(t *testing.T) {
	l, c := newTestLimiter(10, 10, time.Second)
	for i := range pruneAbove + 10 {
		l.outcome(string(rune('a'+i%26))+time.Duration(i).String(), false)
	}
	c.advance(time.Second + maxCooldown + time.Millisecond)
	l.outcome("/fresh", false)
	if n := l.claimCount(); n != 1 {
		t.Errorf("%d claims kept, want 1", n)
	}
}

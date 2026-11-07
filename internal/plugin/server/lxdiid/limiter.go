package lxdiid

import (
	"sync"
	"time"
)

// limiter enforces SEC-09: a cap on concurrent attestations and a global
// token bucket. Nothing is keyed on the claim, which is unauthenticated
// until the proof. It outlives a reconfiguration: update changes its
// limits, and the attestations running keep their slots.
type limiter struct {
	now func() time.Time

	mu      sync.Mutex
	max     int // max_inflight
	running int
	rate    float64 // tokens per second, and the burst
	tokens  float64
	last    time.Time
}

func newLimiter(l limitSettings, now func() time.Time) *limiter {
	return &limiter{
		now:    now,
		max:    l.maxInflight,
		rate:   float64(l.ratePerSec),
		tokens: float64(l.ratePerSec),
		last:   now(),
	}
}

// update applies new limits to the attestations that follow.
func (l *limiter) update(s limitSettings) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.max, l.rate = s.maxInflight, float64(s.ratePerSec)
	l.tokens = min(l.tokens, l.rate)
}

// acquire takes a slot and a token, or refuses at once; release frees the
// slot.
func (l *limiter) acquire() (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running >= l.max || !l.take() {
		return nil, false
	}
	l.running++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.running--
	}, true
}

// inflight is the number of attestations running.
func (l *limiter) inflight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// take takes a token from the bucket, refilled at rate per second; l.mu is
// held.
func (l *limiter) take() bool {
	now := l.now()
	l.tokens = min(l.rate, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

package lxdiid

import (
	"sync"
	"time"
)

// limiter enforces SEC-09: a cap on concurrent attestations and a global
// token bucket. Nothing is keyed on the claim, which is unauthenticated
// until the proof.
type limiter struct {
	now   func() time.Time
	slots chan struct{}

	mu     sync.Mutex
	rate   float64 // tokens per second, and the burst
	tokens float64
	last   time.Time
}

func newLimiter(l limitSettings, now func() time.Time) *limiter {
	return &limiter{
		now:    now,
		slots:  make(chan struct{}, l.maxInflight),
		rate:   float64(l.ratePerSec),
		tokens: float64(l.ratePerSec),
		last:   now(),
	}
}

// acquire takes a slot and a token, or refuses at once; release frees the
// slot.
func (l *limiter) acquire() (release func(), ok bool) {
	select {
	case l.slots <- struct{}{}:
	default:
		return nil, false
	}
	if !l.take() {
		<-l.slots
		return nil, false
	}
	return func() { <-l.slots }, true
}

// inflight is the number of attestations running.
func (l *limiter) inflight() int { return len(l.slots) }

// take takes a token from the bucket, refilled at rate per second.
func (l *limiter) take() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.tokens = min(l.rate, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

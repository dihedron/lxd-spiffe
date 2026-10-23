package lxdiid

import (
	"sync"
	"time"
)

// maxCooldown caps the per-claim cooldown (SEC-09).
const maxCooldown = 5 * time.Minute

// pruneAbove is the number of claims kept before idle ones are pruned. The
// table cannot grow faster than the token bucket lets attestations fail,
// and an entry lives at most its cooldown plus maxCooldown: about 12 000
// entries at the default 20 per second.
const pruneAbove = 1024

// limiter enforces SEC-09: a cap on concurrent attestations, a global
// token bucket, and a cooldown per claim after a failure.
type limiter struct {
	now   func() time.Time
	slots chan struct{}

	mu       sync.Mutex
	rate     float64 // tokens per second, and the burst
	tokens   float64
	last     time.Time
	cooldown time.Duration
	claims   map[string]*claimState
}

// claimState is a claim's failures in a row and the end of its cooldown.
type claimState struct {
	failures int
	until    time.Time
}

func newLimiter(l limitSettings, now func() time.Time) *limiter {
	return &limiter{
		now:      now,
		slots:    make(chan struct{}, l.maxInflight),
		rate:     float64(l.ratePerSec),
		tokens:   float64(l.ratePerSec),
		last:     now(),
		cooldown: l.failureCooldown,
		claims:   map[string]*claimState{},
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

// cooling reports whether the claim must wait after a failure.
func (l *limiter) cooling(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.claims[key]
	return st != nil && l.now().Before(st.until)
}

// outcome records an attestation's result for its claim: a success clears
// the claim, a failure doubles its cooldown, up to maxCooldown.
func (l *limiter) outcome(key string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		delete(l.claims, key)
		return
	}
	now := l.now()
	if len(l.claims) >= pruneAbove {
		for k, st := range l.claims {
			if now.After(st.until.Add(maxCooldown)) {
				delete(l.claims, k)
			}
		}
	}
	st := l.claims[key]
	if st == nil {
		st = &claimState{}
		l.claims[key] = st
	}
	st.failures++
	wait := maxCooldown
	if st.failures < 32 {
		wait = min(maxCooldown, l.cooldown<<(st.failures-1))
	}
	st.until = now.Add(wait)
}

func (l *limiter) claimCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.claims)
}

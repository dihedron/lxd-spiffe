package lxdiid

import (
	"errors"
	"sync"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
)

// errBreakerOpen refuses an attestation through a server whose breaker is
// open: no LXD call is made (FR-S15, T11).
var errBreakerOpen = errors.New("the circuit breaker is open after LXD failures")

// breaker is a server's circuit breaker (FR-S15): threshold transport
// failures in a row open it for wait, which doubles at each reopening up to
// maxOpen; then one trial goes through, and its success closes it.
type breaker struct {
	breakerSettings
	now func() time.Time

	mu        sync.Mutex
	failures  int
	wait      time.Duration
	openUntil time.Time
	trial     bool
}

// allow reports whether a call may go to the server, and whether it is the
// trial after an open period.
func (b *breaker) allow() (trial bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.failures < b.threshold:
		return false, nil
	case b.trial || b.now().Before(b.openUntil):
		return false, errBreakerOpen
	}
	b.trial = true
	return true, nil
}

// observe records a call's result: a transport failure counts, anything
// LXD answered (a 404 included) is a success.
func (b *breaker) observe(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !transportFailure(err) {
		b.failures, b.trial, b.wait = 0, false, b.open
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		if b.trial {
			b.wait = min(2*b.wait, b.maxOpen)
		}
		b.trial = false
		b.openUntil = b.now().Add(b.wait)
	}
}

// transportFailure tells the failures that say LXD is unwell from answers.
func transportFailure(err error) bool {
	switch lxd.KindOf(err) {
	case lxd.KindNone, lxd.KindAuth, lxd.KindNotFound, lxd.KindPrecondition, lxd.KindAgentNotRunning:
		return false
	}
	return !errors.Is(err, lxd.ErrMissingExtensions)
}

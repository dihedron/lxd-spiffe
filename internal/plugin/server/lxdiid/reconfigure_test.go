package lxdiid

import (
	"context"
	"strings"
	"testing"
	"time"

	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
)

func reconfigure(t *testing.T, p *Plugin, hcl string) {
	t.Helper()
	if _, err := p.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration:  hcl,
		CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSEC09ReconfigureKeepsLimits: attestations running when SPIRE calls
// Configure again keep their slots in the limits that follow, so that no
// more than max_inflight run at once (SEC-09).
func TestSEC09ReconfigureKeepsLimits(t *testing.T) {
	f := newTestFiles(t)
	hcl := hclConfig(f, "limits {\n  max_inflight = 1\n}", "")
	p, err := configure(t, hcl)
	if err != nil {
		t.Fatal(err)
	}
	release, ok := p.current().limiter.acquire()
	if !ok {
		t.Fatal("no slot")
	}
	reconfigure(t, p, hcl)
	if _, ok := p.current().limiter.acquire(); ok {
		t.Error("a second attestation got a slot after the reconfiguration")
	}
	release()
	if _, ok := p.current().limiter.acquire(); !ok {
		t.Error("no slot after the first attestation ended")
	}
}

// TestFRS15ReconfigureKeepsServer: an unchanged server entry keeps its
// client and breaker; a changed entry, or changed breaker settings, gets a
// new one (FR-S15).
func TestFRS15ReconfigureKeepsServer(t *testing.T) {
	f := newTestFiles(t)
	hcl := hclConfig(f, "", "")
	p, err := configure(t, hcl)
	if err != nil {
		t.Fatal(err)
	}
	first := p.current().servers[0]
	reconfigure(t, p, hcl)
	if p.current().servers[0] != first {
		t.Error("an unchanged entry got a new server")
	}
	reconfigure(t, p, hclConfig(f, "limits {\n  breaker_threshold = 3\n}", ""))
	second := p.current().servers[0]
	if second == first || second.breaker.threshold != 3 {
		t.Error("new breaker settings kept the old server")
	}
	reconfigure(t, p, strings.Replace(hclConfig(f, "limits {\n  breaker_threshold = 3\n}", ""), `["spire-nodes"]`, `["spire-nodes", "other"]`, 1))
	if p.current().servers[0] == second {
		t.Error("a changed entry kept the old server")
	}
}

// TestSEC09LimiterUpdate: new limits apply to the next attestations; those
// running keep their slots.
func TestSEC09LimiterUpdate(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	l := newLimiter(limitSettings{maxInflight: 2, ratePerSec: 100}, c.now)
	r1, _ := l.acquire()
	r2, _ := l.acquire()
	l.update(limitSettings{maxInflight: 1, ratePerSec: 100})
	r1()
	if _, ok := l.acquire(); ok {
		t.Error("a slot over the new max_inflight")
	}
	r2()
	if _, ok := l.acquire(); !ok {
		t.Error("no slot under the new max_inflight")
	}
	l.update(limitSettings{maxInflight: 3, ratePerSec: 1})
	if l.inflight() != 1 {
		t.Errorf("%d running, want 1", l.inflight())
	}
	if _, ok := l.acquire(); !ok {
		t.Error("no token right after lowering the rate")
	}
	if _, ok := l.acquire(); ok {
		t.Error("a token over the new rate")
	}
}

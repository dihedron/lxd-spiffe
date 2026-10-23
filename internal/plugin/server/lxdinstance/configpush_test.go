package lxdinstance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	lxdinstancev1 "github.com/dihedron/lxd-spiffe/internal/proto"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const configPush = `proof_mode = "config_push"`

// pushAgent offers config_push only and echoes the challenge key's value
// as devLXD would show it.
func pushAgent(name string) *agent {
	a := newAgent(name)
	a.modes = []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH}
	a.answer = echo(nil)
	return a
}

// echo answers with the key's value, after before (if any) has run.
func echo(before func(h *harness, a *agent)) func(h *harness, a *agent, c *lxdinstancev1.Challenge) *lxdinstancev1.ChallengeResponse {
	return func(h *harness, a *agent, c *lxdinstancev1.Challenge) *lxdinstancev1.ChallengeResponse {
		config, _ := h.fake.Config("default", a.name)
		if before != nil {
			before(h, a)
		}
		return &lxdinstancev1.ChallengeResponse{Version: 1, Mode: lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: []byte(config[c.GetProofKey()])}
	}
}

// patches returns the config of every PATCH the fake received.
func (h *harness) patches() []map[string]*string {
	var out []map[string]*string
	for _, r := range h.fake.Requests() {
		if r.Method != http.MethodPatch {
			continue
		}
		var body struct {
			Config map[string]*string `json:"config"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, body.Config)
	}
	return out
}

// checkClean: no challenge key is left and every PATCH wrote challenge keys
// only (SEC-16).
func (h *harness) checkClean(name string) {
	h.t.Helper()
	config, _ := h.fake.Config("default", name)
	for k := range config {
		if strings.HasPrefix(k, "user.spire.challenge.") {
			h.t.Errorf("challenge key %s left", k)
		}
	}
	for _, p := range h.patches() {
		for k := range p {
			if !strings.HasPrefix(k, "user.spire.challenge.") {
				h.t.Errorf("SEC-16: a PATCH wrote %s", k)
			}
		}
	}
}

// TestModeConfigPush: containers and VMs prove co-location with the
// challenge key; the key is removed afterwards (step 3).
func TestModeConfigPush(t *testing.T) {
	for _, typ := range []string{"container", "virtual-machine"} {
		t.Run(typ, func(t *testing.T) {
			h := newHarness(t, configPush)
			h.addInstance("default", "c1", typ, uuidA)
			attrs, err := h.attest("default", pushAgent("c1"))
			if err != nil {
				t.Fatalf("attestation failed: %v\n%s", err, h.log)
			}
			if attrs.GetSpiffeId() != "spiffe://example.org/spire/agent/lxd_instance/lab/"+uuidA {
				t.Errorf("SPIFFE ID %q", attrs.GetSpiffeId())
			}
			if n := len(h.patches()); n != 2 {
				t.Errorf("%d PATCHes, want the challenge write and the cleanup", n)
			}
			if !strings.Contains(h.log.String(), "proof_mode=config_push") || !strings.Contains(h.log.String(), "config_push_stale_keys_removed_total=0") {
				t.Errorf("log:\n%s", h.log)
			}
			h.checkClean("c1")
		})
	}
}

// TestModeConfigPushRefusals: every failure after the write still removes
// the key; a refusal before the write writes nothing.
func TestModeConfigPushRefusals(t *testing.T) {
	cases := map[string]struct {
		top    string
		setup  func(h *harness, a *agent)
		reason string
		writes int
	}{
		"wrong echo": {setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdinstancev1.Challenge) *lxdinstancev1.ChallengeResponse {
				return &lxdinstancev1.ChallengeResponse{Version: 1, Mode: lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: []byte(strings.Repeat("0", 64))}
			}
		}, reason: "proof_mismatch", writes: 2},
		"answer as file_pull": {setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdinstancev1.Challenge) *lxdinstancev1.ChallengeResponse { return done }
		}, reason: "proof_mismatch", writes: 2},
		"no answer": {top: `proof_timeout = "1s"`, setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdinstancev1.Challenge) *lxdinstancev1.ChallengeResponse { return nil }
		}, reason: "proof_timeout", writes: 2},
		"security.devlxd off": {setup: func(h *harness, _ *agent) {
			h.fake.SetInstanceConfig("default", "c1", "security.devlxd", "false")
		}, reason: "policy_denied"},
		"instance moved during the proof": {setup: func(_ *harness, a *agent) {
			a.answer = echo(func(h *harness, a *agent) { h.fake.SetLocation("default", a.name, "m2") })
		}, reason: "instance_moved", writes: 2},
		"FR-S14 retries exhausted": {setup: func(h *harness, _ *agent) { h.fake.AlwaysStale("default", "c1") }, reason: "precondition_failed", writes: 11},
		"FR-S14 status changed between retries": {setup: func(h *harness, _ *agent) {
			h.fake.AfterNextRead("default", "c1", fakelxd.Change{Status: "Frozen"})
		}, reason: "not_running", writes: 1},
		"FR-S14 location changed between retries": {setup: func(h *harness, _ *agent) {
			h.fake.AfterNextRead("default", "c1", fakelxd.Change{Location: "m2"})
		}, reason: "instance_moved", writes: 1},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			h := newHarness(t, configPush+"\n"+c.top)
			h.addInstance("default", "c1", "container", uuidA)
			a := pushAgent("c1")
			c.setup(h, a)
			_, err := h.attest("default", a)
			checkRefused(t, h, err, c.reason)
			if n := len(h.patches()); n != c.writes {
				t.Errorf("%d PATCHes, want %d", n, c.writes)
			}
			h.checkClean("c1")
		})
	}
}

// TestFRS14RetryBudget: 412 retries never run past proof_timeout.
func TestFRS14RetryBudget(t *testing.T) {
	h := newHarness(t, configPush+"\nproof_timeout = \"1s\"")
	h.addInstance("default", "c1", "container", uuidA)
	h.fake.AlwaysStale("default", "c1")
	start := time.Now()
	_, err := h.attest("default", pushAgent("c1"))
	checkRefused(t, h, err, "precondition_failed")
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("refused after %v, want within proof_timeout", elapsed)
	}
}

// TestFRS14RetrySucceeds: a 412 on the challenge write, then on the
// cleanup, are each retried once with the same value.
func TestFRS14RetrySucceeds(t *testing.T) {
	h := newHarness(t, configPush)
	h.addInstance("default", "c1", "container", uuidA)
	h.fake.StaleAfterNextRead("default", "c1")
	a := pushAgent("c1")
	a.answer = echo(func(h *harness, a *agent) { h.fake.StaleAfterNextRead("default", a.name) })
	if _, err := h.attest("default", a); err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	p := h.patches()
	if len(p) != 4 || *p[0][keyOf(p[0])] != *p[1][keyOf(p[1])] {
		t.Errorf("PATCHes %v, want the write twice with one value, the cleanup twice", p)
	}
	if strings.Contains(h.log.String(), "cleanup failed") {
		t.Errorf("cleanup failed:\n%s", h.log)
	}
	h.checkClean("c1")
}

// keyOf returns the key a challenge write sets.
func keyOf(config map[string]*string) string {
	for k, v := range config {
		if v != nil {
			return k
		}
	}
	return ""
}

// TestStaleKeys: the challenge write removes the instance's stale challenge
// keys and keeps those of attestations in flight (Mode config_push, step 1).
func TestStaleKeys(t *testing.T) {
	h := newHarness(t, configPush)
	h.addInstance("default", "c1", "container", uuidA)
	stale, running := "user.spire.challenge.0123456789abcdef", "user.spire.challenge.fedcba9876543210"
	h.fake.SetInstanceConfig("default", "c1", stale, strings.Repeat("a", 64))
	h.fake.SetInstanceConfig("default", "c1", running, strings.Repeat("b", 64))
	defer h.plugin.inflight.add("lab/default/c1", running)()
	if _, err := h.attest("default", pushAgent("c1")); err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	config, _ := h.fake.Config("default", "c1")
	if _, ok := config[stale]; ok {
		t.Error("stale key kept")
	}
	if config[running] == "" {
		t.Error("the key of the attestation in flight was removed")
	}
	if !strings.Contains(h.log.String(), "config_push_stale_keys_removed_total=1") {
		t.Errorf("stale keys not counted:\n%s", h.log)
	}
}

// TestFRS13CleanupFailure: a cleanup that fails is logged and counted; the
// attestation fails on its re-read of the instance.
func TestFRS13CleanupFailure(t *testing.T) {
	h := newHarness(t, configPush)
	h.addInstance("default", "c1", "container", uuidA)
	a := pushAgent("c1")
	a.answer = echo(func(h *harness, _ *agent) { h.fake.FailAll(http.StatusInternalServerError, "down") })
	_, err := h.attest("default", a)
	checkRefused(t, h, err, "lxd_unavailable")
	if !strings.Contains(h.log.String(), "config_push cleanup failed") || !strings.Contains(h.log.String(), "config_push_cleanup_failures_total=1") {
		t.Errorf("no cleanup failure logged:\n%s", h.log)
	}
}

// TestModeNegotiation: auto takes file_pull when offered, then config_push;
// a fixed mode needs the agent to offer it.
func TestModeNegotiation(t *testing.T) {
	both := []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL, lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH}
	push := []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH}
	pull := []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL}
	for _, c := range []struct {
		server  string
		offered []lxdinstancev1.ProofMode
		want    string // the mode used, "" for a refusal
	}{
		{"auto", both, "file_pull"},
		{"auto", push, "config_push"},
		{"auto", pull, "file_pull"},
		{"config_push", both, "config_push"},
		{"config_push", pull, ""},
		{"file_pull", push, ""},
	} {
		h := newHarness(t, `proof_mode = "`+c.server+`"`)
		h.addInstance("default", "c1", "container", uuidA)
		a := newAgent("c1")
		a.modes = c.offered
		if c.want == "config_push" {
			a.answer = echo(nil)
		}
		_, err := h.attest("default", a)
		switch {
		case c.want == "":
			checkRefused(t, h, err, "policy_denied")
		case err != nil:
			t.Errorf("%s with %v: %v\n%s", c.server, c.offered, err, h.log)
		case !strings.Contains(h.log.String(), "proof_mode="+c.want):
			t.Errorf("%s with %v: not %s:\n%s", c.server, c.offered, c.want, h.log)
		}
	}
}

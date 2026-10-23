package lxdiid

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
)

var down = &lxd.Error{Kind: lxd.KindConnection, Err: errors.New("connection refused")}

// TestFRS15BreakerStates: threshold failures open the breaker; after open
// one trial goes through; a failed trial doubles the wait, up to maxOpen;
// a success closes it. Answers such as a 404 are successes.
func TestFRS15BreakerStates(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	b := &breaker{breakerSettings: breakerSettings{threshold: 2, open: time.Second, maxOpen: 3 * time.Second}, now: c.now, wait: time.Second}
	allowed := func() (bool, bool) {
		trial, err := b.allow()
		return err == nil, trial
	}
	b.observe(down)
	b.observe(&lxd.Error{Kind: lxd.KindNotFound, Err: errors.New("404")})
	b.observe(down)
	if ok, _ := allowed(); !ok {
		t.Fatal("a 404 between two failures did not reset the count")
	}
	b.observe(down)
	if ok, _ := allowed(); ok {
		t.Fatal("two failures in a row did not open the breaker")
	}
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second} {
		c.advance(wait - time.Millisecond)
		if ok, _ := allowed(); ok {
			t.Fatalf("opening %d: allowed before %v", i, wait)
		}
		c.advance(time.Millisecond)
		ok, trial := allowed()
		if !ok || !trial {
			t.Fatalf("opening %d: no trial after %v", i, wait)
		}
		if ok, _ := allowed(); ok {
			t.Fatalf("opening %d: a second call during the trial", i)
		}
		b.observe(down)
	}
	c.advance(3 * time.Second)
	if _, trial := allowed(); !trial {
		t.Fatal("no trial")
	}
	b.observe(nil)
	if ok, trial := allowed(); !ok || trial {
		t.Error("a successful trial did not close the breaker")
	}
}

// TestFRS15MissingExtensionIsNoOutage: a server that answers but lacks an
// extension is refused by the check, not counted by the breaker.
func TestFRS15MissingExtensionIsNoOutage(t *testing.T) {
	if transportFailure(lxd.ErrMissingExtensions) {
		t.Error("a missing extension counted as a transport failure")
	}
	if !transportFailure(down) {
		t.Error("a connection failure did not count")
	}
}

// TestSEC16IdentityWarnings: the lab's recorded identity, an admin, is
// warned about; a fine-grained identity with project grants only is not.
func TestSEC16IdentityWarnings(t *testing.T) {
	var resp struct {
		Body struct {
			Metadata api.IdentityInfo `json:"metadata"`
		} `json:"body"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "fixtures", "6.9", "0002-02-get-1.0-auth-identities-current.response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if w := identityWarnings(&resp.Body.Metadata); len(w) == 0 {
		t.Error("no warning for the recorded admin identity")
	}
	scoped := &api.IdentityInfo{FineGrained: true, EffectivePermissions: []api.Permission{
		{EntityType: "project", EntityReference: "/1.0/projects/spire", Entitlement: "can_view"},
		{EntityType: "instance", EntityReference: "/1.0/instances/c1?project=spire", Entitlement: "can_access_files"},
	}}
	if w := identityWarnings(scoped); len(w) != 0 {
		t.Errorf("a least-privilege identity warned: %v", w)
	}
	if w := identityWarnings(&api.IdentityInfo{}); len(w) != 1 {
		t.Errorf("a non-fine-grained identity: %v", w)
	}
}

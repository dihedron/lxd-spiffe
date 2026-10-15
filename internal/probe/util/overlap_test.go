package util

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func TestOverlap(t *testing.T) {
	dir := t.TempDir()
	root := map[string]string{"type": "disk", "path": "/", "pool": "default"}
	shared := map[string]string{"type": "disk", "source": "/srv/l12", "path": "/run/lxd-probe"}
	a := writeRecord(t, dir, "a", "server/0001", map[string]any{
		"instance.expanded_devices": map[string]map[string]string{"root": root, "proof": shared},
	})
	b := writeRecord(t, dir, "b", "server/0002", map[string]any{
		"instance.expanded_devices": map[string]map[string]string{"root": root, "other": {"type": "disk", "source": "/srv/l12", "path": "/mnt"}},
	})
	var out bytes.Buffer
	cmd := &Overlap{GlobalOptions: utilOpts(t), ProofDir: "/run/lxd-probe", stdout: &out}
	cmd.Args.Records = []string{a, b}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := lastRecord(t, &out)
	// a reject is the check working: not a failure
	if rec.Outcome != evidence.OutcomeOK || rec.Verb != "overlap" {
		t.Errorf("record %q %s", rec.Verb, rec.Outcome)
	}
	var results []struct {
		Record  string `json:"record"`
		Verdict string `json:"verdict"`
		Causes  []struct{ Device, Relation string }
	}
	data, _ := json.Marshal(obs(t, rec, "overlap.results"))
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Verdict != "reject" || results[1].Verdict != "pass" {
		t.Fatalf("results = %+v", results)
	}
	if c := results[0].Causes; len(c) != 1 || c[0].Device != "proof" || c[0].Relation != "equal" {
		t.Errorf("causes of A = %+v", c)
	}
	// B mounts the same host directory elsewhere: the check passes it, and
	// the shared source shows the pair
	var sharedSources map[string][]string
	data, _ = json.Marshal(obs(t, rec, "overlap.shared_sources"))
	if err := json.Unmarshal(data, &sharedSources); err != nil {
		t.Fatal(err)
	}
	if got := sharedSources["host:/srv/l12"]; len(got) != 2 {
		t.Errorf("shared sources = %v", sharedSources)
	}
	if obs(t, rec, "overlap.proof_dir") != "/run/lxd-probe" {
		t.Error("proof dir not recorded")
	}

	// a record without expanded_devices is a usage error
	bad := writeRecord(t, dir, "bad", "server/0003", map[string]any{"instance.type": "container"})
	cmd = &Overlap{GlobalOptions: utilOpts(t), ProofDir: "/run/lxd-probe", stdout: &bytes.Buffer{}}
	cmd.Args.Records = []string{bad}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("record without devices: exit %d (%v)", exit.Code(err), err)
	}
}

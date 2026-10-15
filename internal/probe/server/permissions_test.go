package server

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

func permissions(t *testing.T, fake *fakelxd.Server, project string) (evidence.Record, error) {
	t.Helper()
	opts := options(t, fake)
	opts.Project = project
	cmd := &Permissions{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	err := cmd.Execute(nil)
	return record(t, opts, 1), err
}

func verdicts(t *testing.T, rec evidence.Record) map[string]string {
	t.Helper()
	out := map[string]string{}
	data, _ := json.Marshal(observation(t, rec, "permissions"))
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("permissions: %v", err)
	}
	return out
}

func TestPermissions(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", nil)
	fake.AddFile("default", "c1", "/", fakelxd.File{Mode: 0o755, Type: "directory", Entries: []string{"run"}})
	rec, err := permissions(t, fake, "default")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"instance": "allowed", "state": "allowed", "files": "allowed", "events": "allowed", "operations": "allowed", "identity": "allowed"}
	if got := verdicts(t, rec); !equalMaps(got, want) {
		t.Errorf("full access: %v; want %v", got, want)
	}
	if rec.Verb != "permissions" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s", rec.Verb, rec.Outcome)
	}

	// refusals are the data: a 403 does not make the command fail
	denied := fakelxd.NewAssumed(t)
	denied.AddInstance("default", "c1", nil)
	denied.AddFile("default", "c1", "/", fakelxd.File{Mode: 0o755, Type: "directory", Entries: []string{"run"}})
	denied.Deny("/1.0/instances/c1/files")
	denied.Deny("/1.0/events")
	rec, err = permissions(t, denied, "default")
	if err != nil {
		t.Fatalf("a refusal failed the command: %v", err)
	}
	got := verdicts(t, rec)
	if got["files"] != "denied" || got["events"] != "denied" || got["instance"] != "allowed" {
		t.Errorf("with files and events denied: %v", got)
	}

	hidden := fakelxd.NewAssumed(t)
	hidden.AddInstance("restricted", "c1", nil)
	hidden.HideProject("restricted")
	rec, err = permissions(t, hidden, "restricted")
	if err != nil {
		t.Fatal(err)
	}
	if got := verdicts(t, rec); got["instance"] != "hidden" || got["state"] != "hidden" {
		t.Errorf("hidden project: %v", got)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

package util

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func writeFixture(t *testing.T, dir, stem string, req, resp any) {
	t.Helper()
	for suffix, v := range map[string]any{".request.json": req, ".response.json": resp} {
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, stem+suffix), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFixturesVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "6.9")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "trusted",
		"environment": map[string]any{"server_version": "6.9", "server_clustered": false}}
	writeFixture(t, dir, "0001-01-get-1.0",
		map[string]any{"method": "GET", "path": "/1.0"},
		map[string]any{"status": 200, "body": map[string]any{"type": "sync", "metadata": server}})
	run := func() (evidence.Record, error) {
		var out bytes.Buffer
		cmd := &FixturesVerify{GlobalOptions: utilOpts(t), stdout: &out}
		cmd.Args.Dirs = []string{dir}
		err := cmd.Execute(nil)
		if err != nil {
			return evidence.Record{}, err
		}
		return lastRecord(t, &out), nil
	}
	rec, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeOK || rec.Verb != "fixtures verify" || obs(t, rec, "fixtures.total") != float64(1) {
		t.Errorf("record %s %s %v", rec.Verb, rec.Outcome, rec.Observations)
	}

	// an instance without its ETag breaks the contract: a finding
	writeFixture(t, dir, "0002-01-get-1.0-instances-c1",
		map[string]any{"method": "GET", "path": "/1.0/instances/c1", "query": "project=default"},
		map[string]any{"status": 200, "body": map[string]any{"type": "sync", "metadata": map[string]any{"name": "c1"}}})
	rec, err = run()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("outcome %s; want unexpected", rec.Outcome)
	}
	failed, _ := obs(t, rec, "fixtures.failed").([]any)
	if len(failed) != 1 {
		t.Fatalf("fixtures.failed = %v", failed)
	}
	if f, _ := failed[0].(map[string]any); f["fixture"] != "6.9/0002-01-get-1.0-instances-c1" {
		t.Errorf("failed fixture %v", f["fixture"])
	}

	cmd := &FixturesVerify{GlobalOptions: utilOpts(t), stdout: &bytes.Buffer{}}
	cmd.Args.Dirs = []string{filepath.Join(dir, "missing")}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("missing directory: exit %d (%v)", exit.Code(err), err)
	}
}

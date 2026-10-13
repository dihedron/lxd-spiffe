package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// fileFake is an assumed LXD with a container c1 holding a secret file, a
// lab proof, a symlink and a directory.
func fileFake(t *testing.T) *fakelxd.Server {
	t.Helper()
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", nil)
	fake.AddFile("default", "c1", "/etc/secret", fakelxd.File{Content: "s3cret", UID: 0, GID: 0, Mode: 0o600, Type: "file"})
	fake.AddFile("default", "c1", "/run/lxd-probe/proof", fakelxd.File{Content: "nonce-1", UID: 0, GID: 0, Mode: 0o600, Type: "file"})
	fake.AddFile("default", "c1", "/run/lxd-probe/layout/symlink", fakelxd.File{Content: "/etc/hostname", UID: 0, GID: 0, Mode: 0o777, Type: "symlink"})
	fake.AddFile("default", "c1", "/run/lxd-probe/layout", fakelxd.File{UID: 0, GID: 0, Mode: 0o700, Type: "directory", Entries: []string{"directory", "regular", "symlink"}})
	return fake
}

// contains reports whether text appears in a file under dir.
func contains(t *testing.T, dir, text string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err == nil && !d.IsDir() && d.Name() != "aliases.json" {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), text) {
				found = append(found, path)
			}
		}
		return nil
	})
	return found
}

func fileOpts(t *testing.T, fake *fakelxd.Server) probe.ServerCommand {
	t.Helper()
	opts := options(t, fake)
	opts.Record = t.TempDir()
	return opts
}

func TestFileStat(t *testing.T) {
	fake := fileFake(t)
	opts := fileOpts(t, fake)
	cmd := &FileStat{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Verb != "file stat" || rec.Outcome != evidence.OutcomeOK || rec.Server == nil {
		t.Errorf("record %q %s %v", rec.Verb, rec.Outcome, rec.Observations)
	}
	headers, _ := observation(t, rec, "file.headers").(map[string]any)
	for name, want := range map[string]string{"X-Lxd-Uid": "0", "X-Lxd-Gid": "0", "X-Lxd-Mode": "0600", "X-Lxd-Type": "file"} {
		if v, _ := headers[name].([]any); len(v) != 1 || v[0] != want {
			t.Errorf("header %s = %v; want %s (headers %v)", name, headers[name], want, headers)
		}
	}
	if observation(t, rec, "file.size") != float64(6) || observation(t, rec, "file.sha256") == "" {
		t.Errorf("size/hash: %v", rec.Observations)
	}
	// the content reaches neither the evidence nor the fixtures (PRS-35)
	if found := contains(t, opts.EvidenceDir, "s3cret"); len(found) > 0 {
		t.Errorf("content in evidence: %v", found)
	}
	if found := contains(t, opts.Record, "s3cret"); len(found) > 0 {
		t.Errorf("content in fixtures: %v", found)
	}

	// a lab file's content is kept in fixtures only
	cmd = &FileStat{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/run/lxd-probe/proof"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	if found := contains(t, opts.EvidenceDir, "nonce-1"); len(found) > 0 {
		t.Errorf("lab content in evidence: %v", found)
	}
	if found := contains(t, opts.Record, "nonce-1"); len(found) != 1 {
		t.Errorf("lab content in fixtures: %v", found)
	}
}

func TestFileGet(t *testing.T) {
	fake := fileFake(t)
	opts := fileOpts(t, fake)
	cmd := &FileGet{ServerCommand: opts, MaxBytes: 1 << 20, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if observation(t, rec, "file.size") != float64(6) || observation(t, rec, "file.sha256") == "" {
		t.Errorf("get: %v", rec.Observations)
	}
	observation(t, rec, "file.latency_ms")

	// over the limit: recorded, not a failure (PRS-31, PRS-34)
	cmd = &FileGet{ServerCommand: opts, MaxBytes: 3, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("over the limit: %v", err)
	}
	rec = record(t, opts, 2)
	if over, _ := observation(t, rec, "file.over_limit").(map[string]any); over["max_bytes"] != float64(3) {
		t.Errorf("file.over_limit = %v", over)
	}
	if !strings.Contains(strings.Join(rec.Args, " "), "--max-bytes 3") {
		t.Errorf("args %v", rec.Args)
	}
}

func TestFileHeadAndDir(t *testing.T) {
	fake := fileFake(t)
	opts := fileOpts(t, fake)
	head := &FileHead{ServerCommand: opts, stdout: &bytes.Buffer{}}
	head.Args.Name, head.Args.Path = "c1", "/etc/secret"
	if err := head.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Verb != "file head" || observation(t, rec, "file.metadata_without_body") != true || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("head: %s %v", rec.Outcome, rec.Observations)
	}

	dir := &FileDir{ServerCommand: opts, stdout: &bytes.Buffer{}}
	dir.Args.Name, dir.Args.Path = "c1", "/run/lxd-probe/layout"
	if err := dir.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec = record(t, opts, 2)
	entries, _ := observation(t, rec, "file.entries").([]any)
	if rec.Outcome != evidence.OutcomeOK || len(entries) != 3 {
		t.Errorf("dir: %s entries %v, %v", rec.Outcome, entries, rec.Observations)
	}

	// a symlink's answer is metadata: kept
	stat := &FileStat{ServerCommand: opts, stdout: &bytes.Buffer{}}
	stat.Args.Name, stat.Args.Path = "c1", "/run/lxd-probe/layout/symlink"
	if err := stat.Execute(nil); err != nil {
		t.Fatal(err)
	}
	if found := contains(t, opts.EvidenceDir, "/etc/hostname"); len(found) == 0 {
		t.Error("the symlink's target was omitted")
	}
}

func TestFileErrors(t *testing.T) {
	fake := fileFake(t)
	opts := fileOpts(t, fake)
	cmd := &FileStat{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/missing"
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("a 404 is a finding: %v", err)
	}
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeUnexpected || observation(t, rec, "file.error") != "Not found" {
		t.Errorf("missing file: %s %v", rec.Outcome, rec.Observations)
	}

	denied := fileFake(t)
	denied.Deny("/1.0/instances/c1/files")
	opts = fileOpts(t, denied)
	cmd = &FileStat{ServerCommand: opts, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Auth {
		t.Errorf("denied: exit %d (%v)", exit.Code(err), err)
	}
	cmd.ExpectDenied = true
	if err := cmd.Execute(nil); err != nil {
		t.Errorf("--expect-denied: %v", err)
	}
	if rec := record(t, opts, 2); rec.Outcome != evidence.OutcomeOK {
		t.Errorf("--expect-denied: %s %v", rec.Outcome, rec.Observations)
	}
}

// TestFileStatWait checks --wait: retries while the files API fails, one
// artifact and fixture for the final exchange only (PRS-30, L7).
func TestFileStatWait(t *testing.T) {
	fake := fileFake(t)
	fake.FailFiles("default", "c1", 3, http.StatusBadRequest, "VM agent isn't currently running")
	opts := fileOpts(t, fake)
	cmd := &FileStat{ServerCommand: opts, Wait: 10 * time.Second, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeOK {
		t.Errorf("outcome %s: %v", rec.Outcome, rec.Observations)
	}
	if got := observation(t, rec, "wait.attempts"); got != float64(4) {
		t.Errorf("wait.attempts = %v; want 4 (3 failures, then the success)", got)
	}
	if ms, _ := observation(t, rec, "wait.ready_after_ms").(float64); ms < 3*250 {
		t.Errorf("wait.ready_after_ms = %v; want at least three 250 ms intervals", ms)
	}
	failures, _ := observation(t, rec, "wait.failures").([]any)
	if len(failures) != 1 {
		t.Fatalf("wait.failures = %v; want one distinct failure", failures)
	}
	f, _ := failures[0].(map[string]any)
	if f["status"] != float64(400) || f["count"] != float64(3) || f["error"] != "VM agent isn't currently running" {
		t.Errorf("failure = %v", f)
	}
	if observation(t, rec, "file.http_status") != float64(200) {
		t.Error("the final exchange is not the success")
	}
	// GET /1.0 and the final stat: the polls leave no fixtures
	if fixtures, _ := filepath.Glob(filepath.Join(opts.Record, "*", "*.response.json")); len(fixtures) != 2 {
		t.Errorf("fixtures %v; want 2", fixtures)
	}

	// never ready: the last failure is the result, and a finding
	fake.FailFiles("default", "c1", 1000, http.StatusBadRequest, "VM agent isn't currently running")
	opts = fileOpts(t, fake)
	cmd = &FileStat{ServerCommand: opts, Wait: 600 * time.Millisecond, stdout: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Path = "c1", "/etc/secret"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec = record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("never ready: outcome %s", rec.Outcome)
	}
	if observation(t, rec, "wait.not_ready") == nil {
		t.Error("no wait.not_ready")
	}
}

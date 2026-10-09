package evidence

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "update golden files")

func TestNewRunID(t *testing.T) {
	id, err := NewRunID(time.Date(2026, 10, 9, 22, 15, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^20261009T221500Z-[0-9a-f]{4}$`).MatchString(id) {
		t.Errorf("run id = %q", id)
	}
}

func TestOpenValidation(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct{ runID, env string }{
		{"", "server"}, {"..", "server"}, {"a/b", "server"}, {".hidden", "server"},
		{"run1", "laptop"}, {"run1", ""},
	} {
		if _, err := Open(root, tt.runID, tt.env, RunInfo{}); err == nil {
			t.Errorf("Open(%q, %q) succeeded", tt.runID, tt.env)
		}
	}
}

func TestOpenLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	run, err := Open(root, "run1", "server", RunInfo{ToolVersion: "1.2.3", Endpoint: "https://lab:8443"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "run1", "server")
	if run.Dir != dir {
		t.Errorf("Dir = %q; want %q", run.Dir, dir)
	}
	for _, d := range []string{dir, filepath.Join(dir, "records"), filepath.Join(dir, "artifacts")} {
		assertMode(t, d, 0o700)
	}
	assertMode(t, filepath.Join(dir, "run.json"), 0o600)

	var info RunInfo
	readJSON(t, filepath.Join(dir, "run.json"), &info)
	if info.RunID != "run1" || info.Env != "server" || info.ToolVersion != "1.2.3" || info.Endpoint != "https://lab:8443" || info.Schema != Schema {
		t.Errorf("run.json = %+v", info)
	}

	// a later invocation of the same run keeps the first run.json
	if _, err := Open(root, "run1", "server", RunInfo{ToolVersion: "9.9.9"}); err != nil {
		t.Fatal(err)
	}
	readJSON(t, filepath.Join(dir, "run.json"), &info)
	if info.ToolVersion != "1.2.3" {
		t.Errorf("run.json overwritten: %+v", info)
	}
}

func TestRecordWriting(t *testing.T) {
	run, err := Open(t.TempDir(), "run1", "server", RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	run.Sanitizer.AddSecret("s3cr3t-t0ken-value")

	entry, err := run.Begin("config set")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Number != 1 {
		t.Errorf("first number = %d", entry.Number)
	}
	body, err := entry.ArtifactJSON("response", []byte(`{"metadata":{"config":{"user.x":"private"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	text, err := entry.ArtifactText("headers", "Authorization: Bearer s3cr3t-t0ken-value\n")
	if err != nil {
		t.Fatal(err)
	}
	if body != "artifacts/0001-response.json" || text != "artifacts/0001-headers.txt" {
		t.Errorf("artifact paths = %q, %q", body, text)
	}
	rec := &Record{
		Time:    time.Date(2026, 10, 9, 22, 15, 0, 0, time.UTC),
		Verb:    "config set",
		Args:    []string{"c1", "user.lxd-probe.a", "1", "--note=s3cr3t-t0ken-value"},
		Outcome: OutcomeOK,
		Observations: []Observation{
			{Key: "mac", Value: "00:16:3e:aa:bb:cc"},
		},
	}
	if err := entry.Commit(rec); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(run.Dir, "records", "0001-config-set.json")
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Join(run.Dir, body), 0o600)
	var got Record
	readJSON(t, path, &got)
	if got.ID != "server/0001" || got.Env != "server" || got.Schema != Schema {
		t.Errorf("record = %+v", got)
	}
	if strings.Join(got.Args, " ") != "c1 user.lxd-probe.a 1 --note=[redacted]" {
		t.Errorf("args = %v", got.Args)
	}
	if got.Observations[0].Value != "mac-1" {
		t.Errorf("observation = %v", got.Observations[0])
	}
	if len(got.Raw) != 2 || got.Raw[0] != body {
		t.Errorf("raw = %v", got.Raw)
	}
	for _, file := range []string{body, text} {
		data, _ := os.ReadFile(filepath.Join(run.Dir, file))
		if strings.Contains(string(data), "private") || strings.Contains(string(data), "s3cr3t") {
			t.Errorf("%s not sanitized: %s", file, data)
		}
	}

	next, err := run.Begin("info")
	if err != nil {
		t.Fatal(err)
	}
	if next.Number != 2 {
		t.Errorf("second number = %d", next.Number)
	}
	next.Abort()
	if _, err := os.Stat(filepath.Join(run.Dir, "records", "0002-info.json")); !os.IsNotExist(err) {
		t.Errorf("aborted record left behind: %v", err)
	}
}

func TestRecordNumberingConcurrent(t *testing.T) {
	root := t.TempDir()
	const n = 16
	numbers := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := Open(root, "run1", "guest", RunInfo{})
			if err != nil {
				t.Error(err)
				return
			}
			entry, err := run.Begin("preflight")
			if err != nil {
				t.Error(err)
				return
			}
			numbers[i] = entry.Number
			if err := entry.Commit(&Record{Verb: "preflight", Outcome: OutcomeOK}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	sort.Ints(numbers)
	for i, n := range numbers {
		if n != i+1 {
			t.Fatalf("numbers = %v; want 1..%d without gaps or duplicates", numbers, len(numbers))
		}
	}
}

// TestRecordGolden pins the evidence schema (PRT-13).
func TestRecordGolden(t *testing.T) {
	rec := &Record{
		Schema:      Schema,
		ID:          "server/0007",
		Time:        time.Date(2026, 10, 9, 22, 15, 0, 0, time.UTC),
		ToolVersion: "0.0.1",
		Env:         "server",
		Verb:        "file stat",
		Args:        []string{"c1", "/run/lxd-probe/proof"},
		Gate:        "L1",
		Cell:        "lxd6-unpriv-ubuntu-files",
		Outcome:     OutcomeUnexpected,
		DurationMS:  12,
		Server:      &ServerInfo{Version: "6.5", APIExtensionsHash: HashExtensions([]string{"projects", "instance_generation_id"})},
		Expectation: "files-get",
		Observations: []Observation{
			{Key: "header.X-LXD-uid", Value: "1000000"},
			{Key: "expectation.header.X-LXD-type", Value: "missing"},
		},
		Raw: []string{"artifacts/0007-response.json"},
	}
	got, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "record.golden.json")
	if *update {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Errorf("record =\n%s\nwant\n%s", got, want)
	}
}

func TestHashExtensions(t *testing.T) {
	a := HashExtensions([]string{"projects", "instance_generation_id"})
	b := HashExtensions([]string{"instance_generation_id", "projects"})
	if a != b || !strings.HasPrefix(a, "sha256:") {
		t.Errorf("hashes = %q, %q", a, b)
	}
	if a == HashExtensions([]string{"projects"}) {
		t.Errorf("different lists, same hash")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s mode = %o; want %o", path, info.Mode().Perm(), want)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

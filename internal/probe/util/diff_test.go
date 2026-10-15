package util

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

func writeFull(t *testing.T, rec evidence.Record) string {
	t.Helper()
	data, _ := json.Marshal(rec)
	f, err := os.CreateTemp(t.TempDir(), "rec-*.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Close() //nolint:errcheck
	if err := os.WriteFile(f.Name(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestDiff(t *testing.T) {
	base := func(id, version, etag, op string, latency int, mode string) evidence.Record {
		return evidence.Record{
			ID: id, Time: time.Now(), ToolVersion: "x-" + id, Verb: "file stat", Args: []string{"c1", "/run/lxd-probe/proof"},
			Outcome: evidence.OutcomeOK, DurationMS: int64(latency), Server: &evidence.ServerInfo{Version: version},
			Observations: []evidence.Observation{
				{Key: "file.headers", Value: map[string]any{"X-Lxd-Mode": []string{mode}, "X-Lxd-Modified": []string{"t-" + id}, "Etag": []string{etag}}},
				{Key: "file.latency_ms", Value: latency},
				{Key: "write.operation", Value: op},
				{Key: "instance.etag", Value: etag},
			},
		}
	}
	a := writeFull(t, base("server/0001", "5.21.8", "e1", "/1.0/operations/a", 10, "0600"))
	b := writeFull(t, base("server/0007", "6.9", "e2", "/1.0/operations/b", 99, "0640"))

	run := func(ignoreETag bool) evidence.Record {
		out := &bytes.Buffer{}
		cmd := &Diff{GlobalOptions: utilOpts(t), IgnoreETag: ignoreETag, stdout: out}
		cmd.Args.A, cmd.Args.B = a, b
		if err := cmd.Execute(nil); err != nil {
			t.Fatal(err)
		}
		return lastRecord(t, out)
	}
	paths := func(rec evidence.Record) []string {
		var diffs []struct{ Path string }
		data, _ := json.Marshal(obs(t, rec, "diff.differences"))
		_ = json.Unmarshal(data, &diffs)
		var out []string
		for _, d := range diffs {
			out = append(out, d.Path)
		}
		return out
	}
	got := paths(run(false))
	want := []string{"server.version", "observations.file.headers.Etag[0]", "observations.file.headers.X-Lxd-Mode[0]", "observations.instance.etag"}
	if !equal(got, want) {
		t.Errorf("differences %v; want %v (ids, times, *_ms, tool version, operation ids and modified times ignored)", got, want)
	}
	got = paths(run(true))
	want = []string{"server.version", "observations.file.headers.X-Lxd-Mode[0]"}
	if !equal(got, want) {
		t.Errorf("with --ignore-etag: %v; want %v", got, want)
	}
	rec := run(true)
	for _, arg := range rec.Args {
		if filepath.Base(arg) != arg {
			t.Errorf("argument %q is a path", arg)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

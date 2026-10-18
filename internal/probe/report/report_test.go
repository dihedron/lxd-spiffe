package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// testRunbooks is a small runbooks text for the renderer tests.
const testRunbooks = `### Gate runbooks

#### L1 — Files API metadata

**Question:** What does the files API return?

**Decision rules:**
- Header names found → update the contract.
- Owner reported as host uid → translation is normative.

#### L5 — Withdrawn

Kept for reference stability. No runbook.

#### L10 — Client coverage

Non-lab. **Decision:** confirm that every call is covered.

#### L13 — Bearer identities

**Commands:** ` + "`$S bearer check`" + `. **Decision:** whether the plugin can run without certificates.
`

var t0 = time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)

func rec(env string, n int, verb, gate, cell string, outcome evidence.Outcome, at time.Duration, obs ...evidence.Observation) evidence.Record {
	return evidence.Record{
		Schema: 1, ID: env + "/" + pad(n), Time: t0.Add(at), ToolVersion: "test", Env: env, Verb: verb,
		Args: []string{"c-unpriv"}, Gate: gate, Cell: cell, Outcome: outcome, Observations: obs,
	}
}

func pad(n int) string {
	s := "000" + string(rune('0'+n))
	return s[len(s)-4:]
}

func o(key string, v any) evidence.Observation { return evidence.Observation{Key: key, Value: v} }

// writeRun writes records into dir/<run>/<env>/records/ as the probe does.
func writeRun(t *testing.T, dir, run string, recs ...evidence.Record) string {
	t.Helper()
	for _, r := range recs {
		d := filepath.Join(dir, run, r.Env, "records")
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		name := strings.Replace(r.ID[len(r.Env)+1:], "/", "", 1) + "-" + strings.ReplaceAll(r.Verb, " ", "-") + ".json"
		if err := os.WriteFile(filepath.Join(d, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, run)
}

// testEvidence has two runs: L1 with a guest and a server environment
// (merged by time), a decision, an unexpected and an error record; a
// record of a gate the runbooks do not know and one without a gate.
func testEvidence(t *testing.T) []string {
	dir := t.TempDir()
	long := strings.Repeat("x", 300)
	server := rec("server", 1, "file stat", "L1", "lxd6-c-unpriv", evidence.OutcomeOK, 2*time.Second,
		o("file.headers", map[string]any{"X-Lxd-Uid": []string{"0"}}), o("body", long))
	server.Server = &evidence.ServerInfo{Version: "6.9"}
	server.Expectations = []string{"file"}
	run1 := writeRun(t, dir, "20261013T200000Z-g-l1-lxd6",
		rec("guest", 1, "preflight", "L1", "lxd6-c-unpriv", evidence.OutcomeOK, time.Second, o("guest.uid", 0)),
		server,
		rec("guest", 2, "file put", "L1", "lxd6-c-unpriv", evidence.OutcomeOK, 3*time.Second, o("text", "with ```fence`` inside\nand a newline")),
	)
	unexpected := rec("server", 1, "file stat", "L1", "lxd5-v-ubuntu", evidence.OutcomeUnexpected, 0,
		o("expectation.status", map[string]any{"want": []int{200}, "got": 500}), o("file.error", "agent not running"))
	unexpected.Expectations = []string{"file", "server"}
	failed := rec("server", 2, "instance show", "L1", "lxd5-v-ubuntu", evidence.OutcomeError, time.Second, o("error", "connection\nrefused"))
	failed.Expectations = []string{"instance-patch+412"}
	run2 := writeRun(t, dir, "20261013T210000Z-mixed",
		unexpected, failed,
		rec("util", 1, "note", "L1", "all", evidence.OutcomeOK, 2*time.Second, o("note.kind", "decision"), o("note.text", "Owner is the in-container uid: "+long)),
		rec("util", 2, "note", "L13", "lxd5-none", evidence.OutcomeOK, 3*time.Second, o("note.kind", "observation"), o("note.text", "5.21 has no bearer identities")),
		rec("util", 3, "note", "S0", "lxd6", evidence.OutcomeOK, 4*time.Second, o("note.kind", "observation"), o("note.text", "not a gate")),
		rec("util", 4, "diff", "", "", evidence.OutcomeOK, 5*time.Second),
	)
	return []string{run2, run1}
}

func testGates(t *testing.T) []Gate {
	gates, err := parseRunbooks(testRunbooks)
	if err != nil {
		t.Fatal(err)
	}
	return gates
}

func find(t *testing.T, reports []GateReport, id string) GateReport {
	t.Helper()
	for _, r := range reports {
		if r.Gate.ID == id {
			return r
		}
	}
	t.Fatalf("no gate %s", id)
	return GateReport{}
}

func TestLoad(t *testing.T) {
	ev, err := Load(testEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ev.Runs, " ") != "20261013T200000Z-g-l1-lxd6 20261013T210000Z-mixed" {
		t.Errorf("runs %v", ev.Runs)
	}
	var refs []string
	for _, e := range ev.Records[:3] {
		refs = append(refs, e.Ref())
	}
	if want := "20261013T200000Z-g-l1-lxd6/guest/0001 20261013T200000Z-g-l1-lxd6/server/0001 20261013T200000Z-g-l1-lxd6/guest/0002"; strings.Join(refs, " ") != want {
		t.Errorf("environments merged by time: %v", refs)
	}

	dir := t.TempDir()
	if _, err := Load([]string{dir}); !errors.Is(err, ErrNotRun) {
		t.Errorf("empty directory: %v", err)
	}
	runs := testEvidence(t)
	if _, err := Load([]string{runs[0], runs[0]}); err == nil {
		t.Error("a run given twice: no error")
	}
	bad := filepath.Join(dir, "r", "util", "records")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "0001-note.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{filepath.Join(dir, "r")}); err == nil {
		t.Error("malformed record: no error")
	}
}

func TestBuild(t *testing.T) {
	ev, err := Load(testEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	gates := testGates(t)
	gates[0].Axes = []Axis{servers, {"instance", []string{"c-unpriv", "v-ubuntu", "v-alpine"}}}
	reports := Build(ev, gates)
	var ids []string
	for _, r := range reports {
		ids = append(ids, r.Gate.ID)
	}
	if strings.Join(ids, " ") != "L1 L5 L10 L13 (none) S0" {
		t.Fatalf("gates %v", ids)
	}

	l1 := find(t, reports, "L1")
	if l1.Status != StatusDecided || len(l1.Records) != 6 || len(l1.Decisions) != 1 || len(l1.Runs) != 2 {
		t.Errorf("L1 %s records %d decisions %d runs %v", l1.Status, len(l1.Records), len(l1.Decisions), l1.Runs)
	}
	if want := []Cell{{"all", 1, 1}, {"lxd5-v-ubuntu", 2, 1}, {"lxd6-c-unpriv", 3, 1}}; !slices.Equal(l1.Cells, want) {
		t.Errorf("cells %v", l1.Cells)
	}
	if strings.Join(l1.Missing, ",") != "instance v-alpine" {
		t.Errorf("missing %v: the 'all' cell of a note is not real", l1.Missing)
	}
	open := strings.Join(l1.Open, "\n")
	for _, want := range []string{
		"Decided with no real cell for: instance v-alpine.",
		"`20261013T210000Z-mixed/server/0001` file stat (lxd5-v-ubuntu): outcome unexpected; deviations expectation.status.",
		"`20261013T210000Z-mixed/server/0002` instance show (lxd5-v-ubuntu): outcome error: connection refused",
	} {
		if !strings.Contains(open, want) {
			t.Errorf("L1 open items lack %q:\n%s", want, open)
		}
	}
	if strings.Contains(open, "No decision") {
		t.Errorf("L1 is decided:\n%s", open)
	}

	if l5 := find(t, reports, "L5"); l5.Status != StatusWithdrawn || len(l5.Open) != 0 {
		t.Errorf("L5 %s %v", l5.Status, l5.Open)
	}
	l13 := find(t, reports, "L13")
	if l13.Status != StatusUndecided || l13.Open[0] != "No decision recorded (`util note --decision`)." {
		t.Errorf("L13 %s %v", l13.Status, l13.Open)
	}
	l10 := find(t, reports, "L10")
	if l10.Status != StatusNoEvidence {
		t.Errorf("L10 %s", l10.Status)
	}
	reached := map[string]int{}
	for _, c := range l10.Calls {
		reached[c.Method+" "+c.Path] = c.Records
	}
	// file (twice) and server (once) are expectations; instance-patch+412
	// reaches PATCH; nothing reached instances/{name} or identities.
	if reached["GET /1.0/instances/{name}/files"] != 2 || reached["GET /1.0"] != 1 || reached["PATCH /1.0/instances/{name}"] != 1 ||
		reached["GET /1.0/instances/{name}"] != 0 || reached["GET /1.0/auth/identities/current"] != 0 {
		t.Errorf("L10 calls %v", reached)
	}
	if !slices.Contains(l10.Open, "No record reached `GET /1.0/auth/identities/current` (Own identity (optional)).") {
		t.Errorf("L10 open %v", l10.Open)
	}
	if s0 := find(t, reports, "S0"); s0.Known || !slices.Contains(s0.Open, "The runbooks have no gate S0.") {
		t.Errorf("S0 %v %v", s0.Known, s0.Open)
	}
}

// Golden-file test of the renderer (PRT-13).
func TestRenderGolden(t *testing.T) {
	ev, err := Load(testEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	gates := testGates(t)
	gates[0].Axes = []Axis{servers, {"instance", []string{"c-unpriv", "v-ubuntu", "v-alpine"}}}
	out := &bytes.Buffer{}
	if err := Render(out, ev, Build(ev, gates)); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.golden.md")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil { //nolint:gosec // G306: a test file
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if out.String() != string(want) {
		t.Errorf("report differs from %s (run with -update and review the diff):\n%s", golden, out.String())
	}
}

func TestWriteDir(t *testing.T) {
	ev, err := Load(testEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	reports := Build(ev, testGates(t))
	reports = append(reports, GateReport{Gate: Gate{ID: "../evil"}})
	dir := filepath.Join(t.TempDir(), "report")
	if err := WriteDir(dir, ev, reports); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "L1.md L10.md L13.md L5.md S0.md ___evil.md index.md no-gate.md" {
		t.Errorf("files %v", names)
	}
	index, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "| [L1](L1.md) | Files API metadata | decided | 6 | 3 | 1 |") {
		t.Errorf("index:\n%s", index)
	}
	if info, err := os.Stat(filepath.Join(dir, "L1.md")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("L1.md mode %v %v", info, err)
	}
}

// The committed evidence renders, and the gates' statuses are those of the
// decisions recorded so far.
func TestCommittedEvidence(t *testing.T) {
	runs, err := filepath.Glob("../../../docs/validation/*")
	if err != nil || len(runs) == 0 {
		t.Skipf("no committed evidence (%v)", err)
	}
	ev, err := Load(runs)
	if err != nil {
		t.Fatal(err)
	}
	gates, err := Gates()
	if err != nil {
		t.Fatal(err)
	}
	reports := Build(ev, gates)
	for _, r := range reports {
		if !r.Known {
			t.Errorf("records name gate %q, which the runbooks do not know", r.Gate.ID)
		}
		if r.Status == StatusDecided && len(r.Missing) > 0 {
			t.Logf("%s decided without a real cell for %v", r.Gate.ID, r.Missing)
		}
	}
	// awaitingEvidence: calls whose gate is open, by path, with the gate.
	awaitingEvidence := map[string]string{"/1.0/projects/{name}": "L16"}
	for _, c := range find(t, reports, "L10").Calls {
		gate, open := awaitingEvidence[c.Path]
		switch {
		case c.Records == 0 && !open:
			t.Errorf("no record reached the API usage call %+v", c.Call)
		case c.Records > 0 && open:
			t.Errorf("records reach %+v: gate %s has evidence, drop it from awaitingEvidence", c.Call, gate)
		}
	}
	if err := Render(&bytes.Buffer{}, ev, reports); err != nil {
		t.Fatal(err)
	}
}

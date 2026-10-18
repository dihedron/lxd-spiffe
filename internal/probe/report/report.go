package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// envs are the environments of a run, in the order they are read.
var envs = []string{"guest", "server", "util"}

// Entry is a record of a run.
type Entry struct {
	// Run is the run id, the name of the run directory.
	Run string
	evidence.Record
}

// Ref is the record's id within the report: <run>/<env>/<NNNN>.
func (e Entry) Ref() string {
	return e.Run + "/" + e.ID
}

// Evidence is the merged records of one or more runs.
type Evidence struct {
	// Runs are the run ids, sorted.
	Runs []string
	// Records are sorted by run, then time, then id: within a run the
	// environments are merged by record time.
	Records []Entry
}

// ErrNotRun marks a directory that holds no environment of a run.
var ErrNotRun = errors.New("not a run directory")

// isStressSummary reports whether dir is a stress session's collected
// summary (summary.json and results.jsonl, no environment): Load skips it,
// since its records stay in the working evidence.
func isStressSummary(dir string) bool {
	for _, f := range []string{"summary.json", "results.jsonl"} {
		if info, err := os.Stat(filepath.Join(dir, f)); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// Load reads the records of the run directories (evidence/<run_id>, or a
// committed docs/validation/<run_id>). It only reads.
func Load(dirs []string) (*Evidence, error) {
	ev := &Evidence{}
	for _, dir := range dirs {
		run := filepath.Base(filepath.Clean(dir))
		if slices.Contains(ev.Runs, run) {
			return nil, fmt.Errorf("run %s given twice", run)
		}
		found := false
		for _, env := range envs {
			paths, err := filepath.Glob(filepath.Join(dir, env, "records", "*.json"))
			if err != nil {
				return nil, err
			}
			if info, err := os.Stat(filepath.Join(dir, env, "records")); err == nil && info.IsDir() {
				found = true
			}
			for _, path := range paths {
				rec, err := readRecord(path)
				if err != nil {
					return nil, fmt.Errorf("run %s: %w", run, err)
				}
				ev.Records = append(ev.Records, Entry{Run: run, Record: *rec})
			}
		}
		if !found && isStressSummary(dir) {
			continue // a stress session's summary sits beside the runs (stress spec STR-42)
		}
		if !found {
			return nil, fmt.Errorf("%s: %w", run, ErrNotRun)
		}
		ev.Runs = append(ev.Runs, run)
	}
	slices.Sort(ev.Runs)
	slices.SortStableFunc(ev.Records, func(a, b Entry) int {
		if c := strings.Compare(a.Run, b.Run); c != 0 {
			return c
		}
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return ev, nil
}

func readRecord(path string) (*evidence.Record, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a record of a run the operator names
	if err != nil {
		return nil, err
	}
	var rec evidence.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return &rec, nil
}

// Status of a gate in the report.
type Status string

// Statuses.
const (
	StatusWithdrawn  Status = "withdrawn"
	StatusDecided    Status = "decided"
	StatusUndecided  Status = "evidence, no decision"
	StatusEvidence   Status = "evidence" // a group: no decision expected
	StatusNoEvidence Status = "no evidence"
)

// GateReport is the report of one gate.
type GateReport struct {
	Gate Gate
	// Known is false for a gate the records name but the runbooks do not.
	Known  bool
	Status Status
	// Records are the gate's records, in Evidence order.
	Records []Entry
	// Runs are the runs with records of the gate.
	Runs []string
	// Cells are the cells of the records, sorted.
	Cells []Cell
	// Missing are the relevant axis values no real cell covers, as
	// "axis value".
	Missing []string
	// Decisions are the util note --decision records of the gate.
	Decisions []Entry
	// Open are the open items, as Markdown text.
	Open []string
	// Calls is the coverage of the attestor's API usage (L10 only).
	Calls []CallCoverage
}

// Cell counts the records of one cell.
type Cell struct {
	Name    string
	Records int
	Runs    int
}

// CallCoverage counts the records that reached a call (L10).
type CallCoverage struct {
	Call
	Records int
}

// noGate names the records without a gate.
const noGate = "(none)"

// Build groups the records by gate: the runbook gates in order, then the
// gates the runbooks do not know, sorted. It never matches observations
// to decision rules.
func Build(ev *Evidence, gates []Gate) []GateReport {
	byGate := map[string][]Entry{}
	for _, e := range ev.Records {
		g := e.Gate
		if g == "" {
			g = noGate
		}
		byGate[g] = append(byGate[g], e)
	}
	var out []GateReport
	known := map[string]bool{}
	for _, g := range append(slices.Clone(gates), Groups...) {
		known[g.ID] = true
		out = append(out, build(g, true, byGate[g.ID], ev))
	}
	var unknown []string
	for id := range byGate {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	for _, id := range unknown {
		out = append(out, build(Gate{ID: id}, false, byGate[id], ev))
	}
	return out
}

func build(g Gate, known bool, records []Entry, ev *Evidence) GateReport {
	r := GateReport{Gate: g, Known: known, Records: records}
	cells := map[string]*Cell{}
	cellRuns := map[string]map[string]bool{}
	for _, e := range records {
		if !slices.Contains(r.Runs, e.Run) {
			r.Runs = append(r.Runs, e.Run)
		}
		c := cells[e.Cell]
		if c == nil {
			c = &Cell{Name: e.Cell}
			cells[e.Cell], cellRuns[e.Cell] = c, map[string]bool{}
		}
		c.Records++
		cellRuns[e.Cell][e.Run] = true
		if isDecision(e) {
			r.Decisions = append(r.Decisions, e)
		}
	}
	for name, c := range cells {
		c.Runs = len(cellRuns[name])
		r.Cells = append(r.Cells, *c)
	}
	slices.SortFunc(r.Cells, func(a, b Cell) int { return strings.Compare(a.Name, b.Name) })

	for _, axis := range g.Axes {
		for _, v := range axis.Values {
			if !slices.ContainsFunc(r.Cells, func(c Cell) bool { return realCell(c.Name) && covers(c.Name, v) }) {
				r.Missing = append(r.Missing, axis.Name+" "+v)
			}
		}
	}

	switch {
	case g.Withdrawn:
		r.Status = StatusWithdrawn
	case g.Group && len(records) > 0:
		r.Status = StatusEvidence
	case len(r.Decisions) > 0:
		r.Status = StatusDecided
	case len(records) > 0:
		r.Status = StatusUndecided
	default:
		r.Status = StatusNoEvidence
	}
	if g.ID == "L10" {
		r.Calls = callCoverage(ev)
	}
	r.Open = openItems(r)
	return r
}

func isDecision(e Entry) bool {
	if e.Env != "util" || e.Verb != "note" {
		return false
	}
	return value(e.Record, "note.kind") == "decision"
}

// realCell is false for the cells of notes that apply to a whole gate.
func realCell(cell string) bool {
	return cell != "" && cell != "all"
}

// covers tells whether a cell name contains the axis value as a run of
// hyphen-separated parts.
func covers(cell, value string) bool {
	return strings.Contains("-"+cell+"-", "-"+value+"-")
}

func value(rec evidence.Record, key string) any {
	for _, o := range rec.Observations {
		if o.Key == key {
			return o.Value
		}
	}
	return nil
}

func openItems(r GateReport) []string {
	var open []string
	switch r.Status {
	case StatusWithdrawn:
		return nil
	case StatusNoEvidence:
		if r.Known && !r.Gate.Group {
			open = append(open, "No evidence and no decision.")
		}
	case StatusUndecided:
		open = append(open, "No decision recorded (`util note --decision`).")
	}
	switch {
	case r.Gate.ID == noGate:
		open = append(open, "These records name no gate.")
	case !r.Known:
		open = append(open, "The runbooks have no gate "+r.Gate.ID+".")
	}
	if len(r.Missing) > 0 && len(r.Records) > 0 {
		verb := "Not yet covered by a real cell"
		if r.Status == StatusDecided {
			verb = "Decided with no real cell for"
		}
		open = append(open, verb+": "+strings.Join(r.Missing, ", ")+".")
	}
	for _, e := range r.Records {
		switch e.Outcome {
		case evidence.OutcomeUnexpected:
			open = append(open, fmt.Sprintf("`%s` %s (%s): outcome unexpected; %s.", e.Ref(), e.Verb, cellName(e.Cell), findingKeys(e.Record)))
		case evidence.OutcomeError:
			msg, _ := value(e.Record, "error").(string)
			open = append(open, fmt.Sprintf("`%s` %s (%s): outcome error: %s", e.Ref(), e.Verb, cellName(e.Cell), inline(msg)))
		}
	}
	for _, c := range r.Calls {
		if c.Records == 0 {
			open = append(open, fmt.Sprintf("No record reached `%s %s` (%s).", c.Method, c.Path, c.Purpose))
		}
	}
	return open
}

// findingKeys lists the deviations of an unexpected record: its
// expectation.* and decode.* observations. Other findings are not marked
// as such in the record.
func findingKeys(rec evidence.Record) string {
	var keys []string
	for _, o := range rec.Observations {
		if (strings.HasPrefix(o.Key, "expectation.") || strings.HasPrefix(o.Key, "decode.")) && !slices.Contains(keys, o.Key) {
			keys = append(keys, o.Key)
		}
	}
	if len(keys) == 0 {
		return "see the record's observations"
	}
	return "deviations " + strings.Join(keys, ", ")
}

// callCoverage counts, over every record, the calls of the attestor's API
// usage reached, through the expectations the records name; devLXD calls
// are not LXD API calls.
func callCoverage(ev *Evidence) []CallCoverage {
	calls := map[string]expect.Expectation{}
	for _, x := range expect.Table {
		if x.Envelope != expect.DevLXD {
			calls[x.Name] = x
		}
	}
	out := make([]CallCoverage, len(APIUsage))
	for i, c := range APIUsage {
		out[i].Call = c
	}
	for _, e := range ev.Records {
		reached := make([]bool, len(out))
		for _, name := range e.Expectations {
			base, _, _ := strings.Cut(name, "+")
			x, ok := calls[base]
			if !ok {
				continue
			}
			for i := range out {
				if out[i].Method == x.Method && out[i].Path == x.Path {
					reached[i] = true
				}
			}
		}
		for i := range out {
			if reached[i] {
				out[i].Records++
			}
		}
	}
	return out
}

func cellName(cell string) string {
	if cell == "" {
		return "no cell"
	}
	return cell
}

// inline makes text fit on one Markdown line.
func inline(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

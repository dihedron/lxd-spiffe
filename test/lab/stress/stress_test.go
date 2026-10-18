package stress

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var targets = []Target{{"lxd5", "s-001"}, {"lxd5", "s-002"}, {"lxd6", "s-001"}, {"lxd6", "v-ubuntu"}}

// TestSTR11PlanIsReproducible: the same seed gives the same items; faults
// only where they apply, at about the configured rate.
func TestSTR11PlanIsReproducible(t *testing.T) {
	p := Plan{Seed: 42, Iterations: 2000, FaultRate: 0.2, Faults: []string{"C1", "C4", "C9", "C10"}, Modes: []string{FilePull, ConfigPush}, Targets: targets}
	a, b := p.Items(), p.Items()
	if !slices.Equal(a, b) {
		t.Fatal("the same seed gave different plans")
	}
	p.Seed = 43
	if slices.Equal(a, p.Items()) {
		t.Error("another seed gave the same plan")
	}
	faulted := 0
	for _, it := range a {
		if it.Fault == "" {
			continue
		}
		faulted++
		if it.Fault == "C9" && it.Mode != ConfigPush {
			t.Fatalf("C9 planned for %s", it.Mode)
		}
	}
	if rate := float64(faulted) / float64(len(a)); rate < 0.15 || rate > 0.25 {
		t.Errorf("fault rate %.3f, want about 0.2", rate)
	}
}

// TestSTR60RunSerializesInstances: never two iterations on one instance at
// once, all items run, results in item order, concurrency used.
func TestSTR60RunSerializesInstances(t *testing.T) {
	items := Plan{Seed: 1, Iterations: 200, Modes: []string{FilePull}, Targets: targets}.Items()
	var mu sync.Mutex
	busy := map[Target]bool{}
	var inFlight, peak atomic.Int32
	results := Run(context.Background(), items, 8, func(_ context.Context, it Item) Result {
		mu.Lock()
		if busy[it.Target] {
			t.Errorf("two iterations on %v at once", it.Target)
		}
		busy[it.Target] = true
		mu.Unlock()
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
		mu.Lock()
		busy[it.Target] = false
		mu.Unlock()
		return Result{Item: it, Outcome: Pass}
	})
	if len(results) != len(items) {
		t.Fatalf("%d results for %d items", len(results), len(items))
	}
	for i, r := range results {
		if r.N != items[i].N {
			t.Fatalf("result %d is item %d", i, r.N)
		}
	}
	if peak.Load() < 2 || peak.Load() > int32(len(targets)) {
		t.Errorf("peak concurrency %d, want between 2 and %d", peak.Load(), len(targets))
	}
}

func TestRunStopsWithTheContext(t *testing.T) {
	items := Plan{Seed: 1, Iterations: 100, Modes: []string{FilePull}, Targets: targets}.Items()
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	results := Run(ctx, items, 2, func(_ context.Context, it Item) Result {
		if n.Add(1) == 10 {
			cancel()
		}
		return Result{Item: it, Outcome: Pass}
	})
	if len(results) >= len(items) || len(results) < 10 {
		t.Errorf("%d results after cancelling at 10", len(results))
	}
}

var t0 = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

func res(n int, tg Target, mode, fault string, o Outcome, start, end int) Result {
	return Result{Item: Item{N: n, Target: tg, Mode: mode, Fault: fault}, Outcome: o, Start: at(start), End: at(end),
		Steps: map[string]int64{"resolve": 10, "answer": 900, "read": 20}}
}

func withWindow(r Result, start, end int) Result {
	r.Window = &Window{Fault: r.Fault, Server: r.Target.Server, Instance: r.Target.Instance, Start: at(start), End: at(end)}
	return r
}

func recovery(r Result, end int) Result {
	return Result{Item: r.Item, Outcome: Pass, Recovery: true, Start: at(end - 1), End: at(end)}
}

func violations(s Summary, inv string) []Violation {
	var out []Violation
	for _, v := range s.Violations {
		if v.Invariant == inv {
			out = append(out, v)
		}
	}
	return out
}

var bounds = Bounds{Iteration: 40 * time.Second, Recovery: 60 * time.Second}

// TestINV1NoFalsePass: a must-fail fault never passes; under a server-wide
// fault a pass is legitimate (its recheck is a fresh read compared with the
// first, and an LXD crash changes no identity).
func TestINV1NoFalsePass(t *testing.T) {
	a, b, c := targets[0], targets[1], targets[2]
	c1 := withWindow(res(1, a, FilePull, "C1", Pass, 0, 5), 1, 3)
	c3 := withWindow(res(2, b, FilePull, "C3", Fail, 10, 12), 10, 20)
	results := []Result{
		c1, recovery(c1, 10),
		c3, recovery(c3, 25),
		res(3, a, FilePull, "", Pass, 11, 15), // passed during C3 on lxd5: violation
		res(4, a, FilePull, "", Pass, 15, 22), // ended after C3 was cleared: fine
		res(5, c, FilePull, "", Pass, 11, 15), // another server: not hit
	}
	s := Summarize(results, nil, bounds)
	got := violations(s, "INV-1")
	if len(got) != 1 || got[0].Iteration != 1 {
		t.Errorf("INV-1 violations %+v", got)
	}
}

func TestINV2INV4(t *testing.T) {
	a := targets[0]
	slow := res(1, a, FilePull, "", Pass, 0, 50)
	left := res(2, a, ConfigPush, "", Pass, 60, 61)
	left.Leftovers = 1
	crash := withWindow(res(3, a, ConfigPush, "C4", Fail, 70, 71), 70, 71)
	crash.Leftovers = 1
	s := Summarize([]Result{slow, left, crash, recovery(crash, 75)}, nil, bounds)
	if v := violations(s, "INV-2"); len(v) != 1 || v[0].Iteration != 1 {
		t.Errorf("INV-2 %+v", v)
	}
	if v := violations(s, "INV-4"); len(v) != 1 || v[0].Iteration != 2 {
		t.Errorf("INV-4 %+v", v)
	}
	if s.Orphans["C4"] != 1 {
		t.Errorf("orphans %v", s.Orphans)
	}
}

func TestINV3Recovery(t *testing.T) {
	a := targets[0]
	late := withWindow(res(1, a, FilePull, "C2", Fail, 0, 5), 0, 5)
	never := withWindow(res(2, targets[1], FilePull, "C1", Fail, 0, 5), 0, 5)
	ok := withWindow(res(3, targets[2], FilePull, "C6", Fail, 0, 5), 0, 5)
	s := Summarize([]Result{late, recovery(late, 100), never, ok, recovery(ok, 30)}, nil, bounds)
	if v := violations(s, "INV-3"); len(v) != 2 {
		t.Errorf("INV-3 %+v", v)
	}
}

// TestSTR41Summary: outcomes by mode, fault and failing step; latency of
// unfaulted passes without the guest's wait; the report's text.
func TestSTR41Summary(t *testing.T) {
	a := targets[0]
	var results []Result
	for i := range 100 {
		r := res(i, a, FilePull, "", Pass, i, i+1)
		r.Label = "concurrency 1"
		r.Steps["read"] = int64(i + 1)
		results = append(results, r)
	}
	f := withWindow(res(100, a, FilePull, "C10", Fail, 200, 201), 200, 201)
	f.FailedStep, f.Label = "compare", "concurrency 1"
	results = append(results, f, recovery(f, 202))
	s := Summarize(results, map[string]string{"seed": "42"}, bounds)
	if !s.OK() {
		t.Fatalf("violations %+v", s.Violations)
	}
	if s.Outcomes[FilePull]["none"]["pass"] != 100 || s.Outcomes[FilePull]["C10"]["fail at compare"] != 1 {
		t.Errorf("outcomes %v", s.Outcomes)
	}
	l := s.Load[0]
	// attestor latency = resolve 10 + read (1..100), the answer's 900 left out
	if l.Attestor.Count != 100 || l.Attestor.P50 != 60 || l.Attestor.P95 != 105 || l.Attestor.Max != 110 {
		t.Errorf("attestor latency %+v", l.Attestor)
	}
	var md, js bytes.Buffer
	if err := s.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"All invariants hold", "C10 proof tampered | fail at compare: 1", "| concurrency 1 | file_pull | 101 |", "`seed` = `42`"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("summary.md lacks %q:\n%s", want, md.String())
		}
	}
	if !strings.Contains(js.String(), `"p95_ms": 105`) {
		t.Errorf("summary.json: %s", js.String())
	}
}

func TestWriteResults(t *testing.T) {
	var b bytes.Buffer
	rs := []Result{res(1, targets[0], FilePull, "", Pass, 0, 1), res(2, targets[1], ConfigPush, "C9", Fail, 1, 2)}
	if err := WriteResults(&b, rs); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"fault":"C9"`) || !strings.Contains(lines[0], `"steps_ms":{`) {
		t.Errorf("results.jsonl:\n%s", b.String())
	}
}

// TestSTR30Sweep: the sweep may find the crashes' orphans, no more.
func TestSTR30Sweep(t *testing.T) {
	a := targets[0]
	crash := withWindow(res(1, a, ConfigPush, "C4", Fail, 0, 1), 0, 1)
	crash.Leftovers = 1
	ok := Summarize([]Result{crash, recovery(crash, 5)}, nil, bounds)
	ok.Sweep(1)
	if !ok.OK() || ok.Swept != 1 {
		t.Errorf("one orphan swept: %+v", ok.Violations)
	}
	bad := Summarize([]Result{crash, recovery(crash, 5)}, nil, bounds)
	bad.Sweep(3)
	if v := violations(bad, "INV-4"); len(v) != 1 {
		t.Errorf("three swept for one orphan: %+v", bad.Violations)
	}
}

// C9 races the rehearsal: a pass after the writer stopped is allowed.
func TestC9MayPass(t *testing.T) {
	r := withWindow(res(1, targets[0], ConfigPush, "C9", Pass, 0, 5), 0, 2)
	if s := Summarize([]Result{r, recovery(r, 6)}, nil, bounds); !s.OK() {
		t.Errorf("C9 pass after the writer stopped: %+v", s.Violations)
	}
}

// TestINV3QuietServer: recovery is measured from when the server is quiet:
// crashes that follow within the bound push the deadline on.
func TestINV3QuietServer(t *testing.T) {
	a, b := targets[0], targets[1]
	c5 := withWindow(res(1, a, FilePull, "C5", Fail, 0, 5), 0, 5)
	// crashes elsewhere on lxd5 at 30-40 s and 80-90 s keep the server busy
	c3a := withWindow(res(2, b, FilePull, "C3", Fail, 30, 40), 30, 40)
	c3b := withWindow(res(3, b, FilePull, "C3", Fail, 80, 90), 80, 90)
	late := recovery(c5, 120) // 30 s after the last crash, 115 s after C5
	s := Summarize([]Result{c5, late, c3a, recovery(c3a, 45), c3b, recovery(c3b, 95)}, nil, bounds)
	if v := violations(s, "INV-3"); len(v) != 0 {
		t.Errorf("INV-3 %+v", v)
	}
	// without the crashes, 115 s is too late
	s = Summarize([]Result{c5, late}, nil, bounds)
	if v := violations(s, "INV-3"); len(v) != 1 {
		t.Errorf("INV-3 without crashes %+v", v)
	}
}

// TestINV4Attributed: leftovers under a server-wide fault are its orphans.
func TestINV4Attributed(t *testing.T) {
	a, b := targets[0], targets[1]
	c3 := withWindow(res(1, b, ConfigPush, "C3", Fail, 10, 20), 10, 14)
	hit := res(2, a, ConfigPush, "", Fail, 11, 13)
	hit.Leftovers = 1
	s := Summarize([]Result{c3, recovery(c3, 30), hit}, nil, bounds)
	if !s.OK() || s.Orphans["C3"] != 1 {
		t.Errorf("violations %+v, orphans %v", s.Violations, s.Orphans)
	}
}

// A recovery's leftovers count too: attributed to a fault that hit it.
func TestRecoveryLeftovers(t *testing.T) {
	a, b := targets[0], targets[1]
	c3 := withWindow(res(1, b, ConfigPush, "C3", Fail, 10, 20), 10, 14)
	rec := recovery(c3, 13)
	rec.Target, rec.Start, rec.Leftovers = a, at(11), 1
	s := Summarize([]Result{c3, recovery(c3, 30), rec}, nil, bounds)
	s.Sweep(1)
	if !s.OK() || s.Orphans["C3"] != 1 {
		t.Errorf("violations %+v, orphans %v", s.Violations, s.Orphans)
	}
}

// A leftover is blamed on the crash or outage that hit the iteration, not
// on its own tampered proof.
func TestLeftoverCause(t *testing.T) {
	a, b := targets[0], targets[1]
	c3 := withWindow(res(1, b, ConfigPush, "C3", Fail, 10, 20), 10, 14)
	c10 := withWindow(res(2, a, ConfigPush, "C10", Fail, 11, 13), 11, 13)
	c10.Leftovers = 1
	s := Summarize([]Result{c3, recovery(c3, 30), c10, recovery(c10, 31)}, nil, bounds)
	if s.Orphans["C3"] != 1 || s.Orphans["C10"] != 0 {
		t.Errorf("orphans %v", s.Orphans)
	}
}

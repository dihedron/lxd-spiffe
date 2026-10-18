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

// TestINV1NoFalsePass: a must-fail fault never passes; a server-wide fault
// passes only for iterations that ended after it was cleared, and hits the
// other iterations on its server.
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
	if len(got) != 2 || got[0].Iteration != 1 || got[1].Iteration != 3 {
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

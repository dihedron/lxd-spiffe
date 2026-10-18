// Package stress is the lab-independent part of the stress, soak and chaos
// tests (stress spec, lxd-stress.md): the seeded plan of iterations, the
// worker pool that runs them, the fault catalogue, and the summary with
// its invariants. The lab sessions in test/lab/acceptance give it an
// iteration function that runs a rehearsal and injects the faults.
package stress

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// The proof modes.
const (
	FilePull   = "file_pull"
	ConfigPush = "config_push"
)

// Outcome is how an iteration ended.
type Outcome string

// Outcomes.
const (
	Pass  Outcome = "pass"
	Fail  Outcome = "fail"  // a step failed: FailedStep says which
	Error Outcome = "error" // the harness could not run the iteration
)

// Scope is what a fault affects.
type Scope string

// Scopes.
const (
	ScopeInstance Scope = "instance" // only the iteration that injects it
	ScopeServer   Scope = "server"   // every iteration on the server while it lasts
)

// Fault describes a fault of the catalogue (stress spec, Fault catalogue).
type Fault struct {
	ID    string
	Name  string
	Scope Scope
	// MustFail: an iteration it hits never passes (INV-1). Otherwise a hit
	// iteration passes only if it ended after the fault was cleared.
	MustFail bool
	// Crash: the fault kills a half of the rehearsal, so leftovers are
	// orphans, not violations (INV-4).
	Crash bool
	// Modes the fault applies to; empty means both.
	Modes   []string
	Cluster bool // needs a cluster
}

// Catalogue is the fault catalogue, C1 to C10.
var Catalogue = []Fault{
	{ID: "C1", Name: "instance stopped", Scope: ScopeInstance, MustFail: true},
	{ID: "C2", Name: "instance restarted", Scope: ScopeInstance, MustFail: true},
	{ID: "C3", Name: "LXD daemon restarted", Scope: ScopeServer},
	{ID: "C4", Name: "server half killed", Scope: ScopeInstance, MustFail: true, Crash: true},
	{ID: "C5", Name: "guest half killed", Scope: ScopeInstance, MustFail: true},
	{ID: "C6", Name: "network partition", Scope: ScopeServer},
	{ID: "C7", Name: "cluster member stopped", Scope: ScopeServer, Cluster: true},
	{ID: "C8", Name: "instance moved", Scope: ScopeInstance, MustFail: true, Cluster: true},
	{ID: "C9", Name: "concurrent writer", Scope: ScopeInstance, MustFail: true, Modes: []string{ConfigPush}},
	{ID: "C10", Name: "proof tampered", Scope: ScopeInstance, MustFail: true},
}

// Lookup returns the catalogue's fault id.
func Lookup(id string) (Fault, bool) {
	for _, f := range Catalogue {
		if f.ID == id {
			return f, true
		}
	}
	return Fault{}, false
}

func (f Fault) applies(mode string) bool {
	if len(f.Modes) == 0 {
		return true
	}
	for _, m := range f.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Target is one fleet instance.
type Target struct {
	Server   string `json:"server"`
	Instance string `json:"instance"`
}

// Item is one planned iteration.
type Item struct {
	N      int    `json:"n"`
	Target Target `json:"target"`
	Mode   string `json:"mode"`
	Fault  string `json:"fault,omitempty"`
}

// Plan is a soak (STR-11): the same seed gives the same items.
type Plan struct {
	Seed       uint64
	Iterations int
	FaultRate  float64
	Faults     []string // fault ids to draw from
	Modes      []string
	Targets    []Target
}

// Items draws the plan's iterations: a target uniformly, the modes in
// turn, and with probability FaultRate one of the faults that apply to the
// mode.
func (p Plan) Items() []Item {
	r := rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))
	items := make([]Item, 0, p.Iterations)
	for n := range p.Iterations {
		it := Item{N: n, Target: p.Targets[r.IntN(len(p.Targets))], Mode: p.Modes[n%len(p.Modes)]}
		if r.Float64() < p.FaultRate {
			var usable []string
			for _, id := range p.Faults {
				if f, ok := Lookup(id); ok && f.applies(it.Mode) {
					usable = append(usable, id)
				}
			}
			if len(usable) > 0 {
				it.Fault = usable[r.IntN(len(usable))]
			}
		}
		items = append(items, it)
	}
	return items
}

// Window is the time a fault lasted, from injection to clean-up.
type Window struct {
	Fault    string    `json:"fault"`
	Server   string    `json:"server"`
	Instance string    `json:"instance,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
}

// Result is one iteration's result.
type Result struct {
	Item
	Label      string           `json:"label,omitempty"` // the load step, as "concurrency 10"
	Outcome    Outcome          `json:"outcome"`
	FailedStep string           `json:"failed_step,omitempty"`
	Steps      map[string]int64 `json:"steps_ms,omitempty"`
	Start      time.Time        `json:"start"`
	End        time.Time        `json:"end"`
	// Window is the fault this iteration injected, if any.
	Window *Window `json:"window,omitempty"`
	// Leftovers counts challenge keys and proof files left after it.
	Leftovers int `json:"leftovers,omitempty"`
	// Recovery marks an iteration run after a fault's clean-up to show
	// that the instance passes again (INV-3); Attempts counts its tries.
	Recovery bool   `json:"recovery,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Duration is how long the iteration took.
func (r Result) Duration() time.Duration { return r.End.Sub(r.Start) }

// AttestorMS is the attestor's own latency: the sum of its steps but the
// wait for the guest half (PRS-54, STR-40).
func (r Result) AttestorMS() int64 {
	var total int64
	for step, ms := range r.Steps {
		if step != "answer" {
			total += ms
		}
	}
	return total
}

// Run runs items on workers goroutines, never two on the same instance at
// once, and returns the results in item order. iterate runs one item; ctx
// ends the run early (the remaining items are not run).
func Run(ctx context.Context, items []Item, workers int, iterate func(context.Context, Item) Result) []Result {
	results := make([]Result, len(items))
	done := make([]bool, len(items))
	var mu sync.Mutex
	busy := map[Target]bool{}
	cond := sync.NewCond(&mu)
	next := 0 // the first item not yet taken
	taken := make([]bool, len(items))

	// take returns the first untaken item whose instance is free, waiting
	// while all of them are busy; -1 when nothing is left.
	take := func() int {
		mu.Lock()
		defer mu.Unlock()
		for {
			if ctx.Err() != nil {
				return -1
			}
			for next < len(items) && taken[next] {
				next++
			}
			if next == len(items) {
				return -1
			}
			for i := next; i < len(items); i++ {
				if !taken[i] && !busy[items[i].Target] {
					taken[i], busy[items[i].Target] = true, true
					return i
				}
			}
			cond.Wait()
		}
	}
	release := func(i int, r Result) {
		mu.Lock()
		results[i], done[i] = r, true
		busy[items[i].Target] = false
		mu.Unlock()
		cond.Broadcast()
	}
	stop := context.AfterFunc(ctx, cond.Broadcast)
	defer stop()

	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Go(func() {
			for i := take(); i >= 0; i = take() {
				release(i, iterate(ctx, items[i]))
			}
		})
	}
	wg.Wait()
	out := results[:0]
	for i, r := range results {
		if done[i] {
			out = append(out, r)
		}
	}
	return out
}

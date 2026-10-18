package stress

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

// Bounds are the limits the invariants check.
type Bounds struct {
	// Iteration bounds one iteration's duration (INV-2: --timeout + 10 s).
	Iteration time.Duration
	// Recovery bounds the time from a fault's clean-up to the recovery
	// iteration that passes (INV-3: 60 s).
	Recovery time.Duration
}

// Violation is a broken invariant.
type Violation struct {
	Invariant string `json:"invariant"`
	Iteration int    `json:"iteration"`
	Target    Target `json:"target"`
	Detail    string `json:"detail"`
}

// Latency is a set of percentiles, in milliseconds.
type Latency struct {
	Count int   `json:"count"`
	P50   int64 `json:"p50_ms"`
	P95   int64 `json:"p95_ms"`
	P99   int64 `json:"p99_ms"`
	Max   int64 `json:"max_ms"`
}

// LoadStep is the latency and throughput of one load step and mode.
type LoadStep struct {
	Label      string             `json:"label"`
	Mode       string             `json:"mode"`
	Attestor   Latency            `json:"attestor"`
	Steps      map[string]Latency `json:"steps"`
	Iterations int                `json:"iterations"`
	PerSecond  float64            `json:"per_second"`
}

// Summary is the result of a run (STR-41).
type Summary struct {
	Settings map[string]string `json:"settings"`
	// Outcomes counts mode → fault ("none" without one) → outcome, a
	// failure under "fail at <step>".
	Outcomes   map[string]map[string]map[string]int `json:"outcomes"`
	Load       []LoadStep                           `json:"load"`
	Violations []Violation                          `json:"violations"`
	Orphans    map[string]int                       `json:"orphans"`
	Recoveries int                                  `json:"recoveries"`
	Windows    int                                  `json:"windows"`
}

// OK reports whether no invariant was broken.
func (s Summary) OK() bool { return len(s.Violations) == 0 }

// Summarize computes the summary and checks INV-1 to INV-4 on results.
func Summarize(results []Result, settings map[string]string, b Bounds) Summary {
	s := Summary{
		Settings: settings, Outcomes: map[string]map[string]map[string]int{},
		Violations: []Violation{}, Orphans: map[string]int{},
	}
	var windows []Window
	for _, r := range results {
		if r.Window != nil {
			windows = append(windows, *r.Window)
		}
	}
	s.Windows = len(windows)
	for _, r := range results {
		if r.Recovery {
			s.Recoveries++
			continue
		}
		s.count(r)
		hits := hitBy(r, windows)
		for _, w := range hits {
			f, _ := Lookup(w.Fault)
			switch {
			case r.Outcome != Pass:
			case f.MustFail:
				s.violate("INV-1", r, fmt.Sprintf("passed although hit by %s (%s)", f.ID, f.Name))
			case r.End.Before(w.End):
				s.violate("INV-1", r, fmt.Sprintf("passed while %s (%s) was not cleared", f.ID, f.Name))
			}
		}
		if b.Iteration > 0 && r.Duration() > b.Iteration {
			s.violate("INV-2", r, fmt.Sprintf("took %s, over %s", r.Duration().Round(time.Millisecond), b.Iteration))
		}
		if r.Leftovers > 0 {
			if f, ok := Lookup(r.Fault); ok && f.Crash {
				s.Orphans[f.ID] += r.Leftovers
			} else {
				s.violate("INV-4", r, fmt.Sprintf("%d challenge keys or proof files left", r.Leftovers))
			}
		}
	}
	s.checkRecovery(results, windows, b)
	s.Load = load(results, windows)
	return s
}

func (s *Summary) violate(inv string, r Result, detail string) {
	s.Violations = append(s.Violations, Violation{Invariant: inv, Iteration: r.N, Target: r.Target, Detail: detail})
}

func (s *Summary) count(r Result) {
	fault := r.Fault
	if fault == "" {
		fault = "none"
	}
	key := string(r.Outcome)
	if r.Outcome == Fail {
		key = "fail at " + r.FailedStep
	}
	if s.Outcomes[r.Mode] == nil {
		s.Outcomes[r.Mode] = map[string]map[string]int{}
	}
	if s.Outcomes[r.Mode][fault] == nil {
		s.Outcomes[r.Mode][fault] = map[string]int{}
	}
	s.Outcomes[r.Mode][fault][key]++
}

// hitBy returns the windows that hit r: its own fault's, and those of
// server-wide faults on its server that overlap it.
func hitBy(r Result, windows []Window) []Window {
	var hits []Window
	if r.Window != nil {
		hits = append(hits, *r.Window)
	}
	for _, w := range windows {
		f, _ := Lookup(w.Fault)
		if f.Scope != ScopeServer || w.Server != r.Target.Server || (r.Window != nil && w == *r.Window) {
			continue
		}
		if r.Start.Before(w.End) && w.Start.Before(r.End) {
			hits = append(hits, w)
		}
	}
	return hits
}

// checkRecovery is INV-3: after each window, a recovery iteration on the
// same instance (instance faults) or server (server faults) passes within
// the bound.
func (s *Summary) checkRecovery(results []Result, windows []Window, b Bounds) {
	for _, w := range windows {
		f, _ := Lookup(w.Fault)
		recovered := false
		for _, r := range results {
			if !r.Recovery || r.Outcome != Pass || r.Fault != w.Fault || r.Target.Server != w.Server {
				continue
			}
			if f.Scope == ScopeInstance && r.Target.Instance != w.Instance {
				continue
			}
			if !r.End.Before(w.End) && (b.Recovery == 0 || r.End.Sub(w.End) <= b.Recovery) {
				recovered = true
				break
			}
		}
		if !recovered {
			s.Violations = append(s.Violations, Violation{
				Invariant: "INV-3", Target: Target{Server: w.Server, Instance: w.Instance},
				Detail: fmt.Sprintf("no recovery within %s after %s (%s) ended at %s", b.Recovery, f.ID, f.Name, w.End.UTC().Format(time.RFC3339)),
			})
		}
	}
}

// load computes latency and throughput per load step and mode, over the
// passes that no fault hit.
func load(results []Result, windows []Window) []LoadStep {
	type key struct{ label, mode string }
	groups := map[key][]Result{}
	for _, r := range results {
		if r.Recovery {
			continue
		}
		groups[key{r.Label, r.Mode}] = append(groups[key{r.Label, r.Mode}], r)
	}
	keys := slices.SortedFunc(maps.Keys(groups), func(a, b key) int {
		if c := strings.Compare(a.label, b.label); c != 0 {
			return c
		}
		return strings.Compare(a.mode, b.mode)
	})
	var out []LoadStep
	for _, k := range keys {
		rs := groups[k]
		ls := LoadStep{Label: k.label, Mode: k.mode, Steps: map[string]Latency{}, Iterations: len(rs)}
		var attestor []int64
		steps := map[string][]int64{}
		first, last := rs[0].Start, rs[0].End
		for _, r := range rs {
			first, last = minTime(first, r.Start), maxTime(last, r.End)
			if r.Outcome != Pass || len(hitBy(r, windows)) > 0 {
				continue
			}
			attestor = append(attestor, r.AttestorMS())
			for step, ms := range r.Steps {
				steps[step] = append(steps[step], ms)
			}
		}
		ls.Attestor = percentiles(attestor)
		for step, v := range steps {
			ls.Steps[step] = percentiles(v)
		}
		if d := last.Sub(first).Seconds(); d > 0 {
			ls.PerSecond = float64(len(rs)) / d
		}
		out = append(out, ls)
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// percentiles uses the nearest rank.
func percentiles(v []int64) Latency {
	if len(v) == 0 {
		return Latency{}
	}
	s := slices.Clone(v)
	slices.Sort(s)
	rank := func(p float64) int64 {
		i := int(p*float64(len(s))+0.999999) - 1
		return s[min(max(i, 0), len(s)-1)]
	}
	return Latency{Count: len(s), P50: rank(0.50), P95: rank(0.95), P99: rank(0.99), Max: s[len(s)-1]}
}

// WriteJSON writes the summary as summary.json's content.
func (s Summary) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// WriteMarkdown writes the summary as summary.md's content.
func (s Summary) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# Stress run summary\n\n")
	verdict := "**All invariants hold.**"
	if !s.OK() {
		verdict = fmt.Sprintf("**%d invariant violations.**", len(s.Violations))
	}
	fmt.Fprintf(&b, "%s %d faults injected, %d recovery iterations.\n\n## Settings\n\n", verdict, s.Windows, s.Recoveries)
	for _, k := range slices.Sorted(maps.Keys(s.Settings)) {
		fmt.Fprintf(&b, "- `%s` = `%s`\n", k, s.Settings[k])
	}
	b.WriteString("\n## Outcomes\n\n| Mode | Fault | Outcomes |\n|---|---|---|\n")
	for _, mode := range slices.Sorted(maps.Keys(s.Outcomes)) {
		for _, fault := range slices.Sorted(maps.Keys(s.Outcomes[mode])) {
			counts := s.Outcomes[mode][fault]
			var parts []string
			for _, o := range slices.Sorted(maps.Keys(counts)) {
				parts = append(parts, fmt.Sprintf("%s: %d", o, counts[o]))
			}
			name := fault
			if f, ok := Lookup(fault); ok {
				name = f.ID + " " + f.Name
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", mode, name, strings.Join(parts, ", "))
		}
	}
	b.WriteString("\n## Latency of the attestor's steps (passes without faults)\n\n| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |\n|---|---|---|---|---|---|---|---|\n")
	for _, l := range s.Load {
		fmt.Fprintf(&b, "| %s | %s | %d | %.1f | %d ms | %d ms | %d ms | %d ms |\n",
			l.Label, l.Mode, l.Iterations, l.PerSecond, l.Attestor.P50, l.Attestor.P95, l.Attestor.P99, l.Attestor.Max)
	}
	b.WriteString("\n## Orphans (left by crashes, INV-4)\n\n")
	if len(s.Orphans) == 0 {
		b.WriteString("None.\n")
	}
	for _, id := range slices.Sorted(maps.Keys(s.Orphans)) {
		f, _ := Lookup(id)
		fmt.Fprintf(&b, "- %s (%s): %d\n", id, f.Name, s.Orphans[id])
	}
	b.WriteString("\n## Violations\n\n")
	if len(s.Violations) == 0 {
		b.WriteString("None.\n")
	}
	vs := slices.Clone(s.Violations)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Invariant < vs[j].Invariant })
	for _, v := range vs {
		fmt.Fprintf(&b, "- %s, iteration %d, %s/%s: %s\n", v.Invariant, v.Iteration, v.Target.Server, v.Target.Instance, v.Detail)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

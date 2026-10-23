//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The judgement of the real soak (lab stress spec, STR-53): the invariants
// and the measures come from SPIRE Server's log and from the fleet's state
// after the faults, not from a harness driving each attestation.

// soakAttest is one attestation line of SPIRE Server's log.
type soakAttest struct {
	Time       time.Time
	UUID       string
	Mode       string
	Reason     string
	DurationMS int64
	ProofMS    int64
	OK         bool
}

var kvRE = regexp.MustCompile(`(\w+)=("(?:[^"\\]|\\.)*"|\S+)`)

// parseAttest reads a log line of the plugin's attestation outcome.
func parseAttest(line string) (soakAttest, bool) {
	ok := strings.Contains(line, "attestation succeeded")
	if !ok && !strings.Contains(line, "attestation refused") {
		return soakAttest{}, false
	}
	f := map[string]string{}
	for _, m := range kvRE.FindAllStringSubmatch(line, -1) {
		f[m[1]] = strings.Trim(m[2], `"`)
	}
	at, err := time.Parse(time.RFC3339, f["time"])
	if err != nil {
		return soakAttest{}, false
	}
	d, _ := strconv.ParseInt(f["duration_ms"], 10, 64)
	p, _ := strconv.ParseInt(f["proof_ms"], 10, 64)
	return soakAttest{Time: at, UUID: f["instance_uuid"], Mode: f["proof_mode"], Reason: f["reason_code"], DurationMS: d, ProofMS: p, OK: ok}, true
}

// journal returns the attestations SPIRE Server logged since t.
func (r *soakRun) journal(since time.Time) []soakAttest {
	out := r.l.ssh(r.t, "spire", fmt.Sprintf(`sudo journalctl -u spire-server --since "@%d" --no-pager -o cat | grep -E 'attestation (succeeded|refused)'; true`, since.Unix()))
	var all []soakAttest
	for _, line := range strings.Split(out, "\n") {
		if a, ok := parseAttest(line); ok {
			all = append(all, a)
		}
	}
	return all
}

// soakLatency are percentiles of durations in milliseconds.
type soakLatency struct {
	N                  int      `json:"n"`
	P50, P95, P99, Max int64    `json:"-"`
	Ms                 [4]int64 `json:"p50_p95_p99_max_ms"`
}

func latencies(v []int64) soakLatency {
	if len(v) == 0 {
		return soakLatency{}
	}
	slices.Sort(v)
	at := func(p float64) int64 { return v[min(len(v)-1, int(p*float64(len(v))))] }
	l := soakLatency{N: len(v), P50: at(0.5), P95: at(0.95), P99: at(0.99), Max: v[len(v)-1]}
	l.Ms = [4]int64{l.P50, l.P95, l.P99, l.Max}
	return l
}

// soakSummary is the soak's result.
type soakSummary struct {
	Settings    map[string]string      `json:"settings"`
	Environment string                 `json:"environment"`
	Succeeded   map[string]int         `json:"succeeded_by_mode"`
	Refused     map[string]int         `json:"refused_by_reason"`
	Duration    map[string]soakLatency `json:"duration_ms_by_mode"`
	Proof       map[string]soakLatency `json:"proof_ms_by_mode"`
	PerSecond   float64                `json:"attestations_per_second"`
	LeftoverKey int                    `json:"challenge_keys_left"`
	Events      []soakEvent            `json:"events"`
	Samples     []soakSample           `json:"samples"`
	Violations  []string               `json:"violations"`
	Findings    []string               `json:"findings"`
}

func (s *soakSummary) violate(inv, format string, a ...any) {
	s.Violations = append(s.Violations, inv+": "+fmt.Sprintf(format, a...))
}

func (s *soakSummary) find(format string, a ...any) {
	s.Findings = append(s.Findings, fmt.Sprintf(format, a...))
}

// measures fills the counts and latencies from the attestations.
func (s *soakSummary) measures(atts []soakAttest, elapsed time.Duration) {
	s.Succeeded, s.Refused = map[string]int{}, map[string]int{}
	durations, proofs := map[string][]int64{}, map[string][]int64{}
	for _, a := range atts {
		if !a.OK {
			s.Refused[a.Reason]++
			continue
		}
		s.Succeeded[a.Mode]++
		durations[a.Mode] = append(durations[a.Mode], a.DurationMS)
		proofs[a.Mode] = append(proofs[a.Mode], a.ProofMS)
	}
	s.Duration, s.Proof = map[string]soakLatency{}, map[string]soakLatency{}
	total := 0
	for m, v := range durations {
		s.Duration[m], s.Proof[m] = latencies(v), latencies(proofs[m])
		total += len(v)
	}
	s.PerSecond = float64(total) / elapsed.Seconds()
}

// INV-1: no attestation is accepted for an instance while it is stopped, or
// for an instance outside the fleet, or in another proof mode than the
// server's at the time.
func (r *soakRun) checkSafety(s *soakSummary, atts []soakAttest) {
	byUUID := map[string]string{}
	for n, u := range r.uuids {
		byUUID[u] = n
	}
	var switchAt time.Time
	for _, e := range r.events {
		if e.Fault == "mode-switch" {
			switchAt = e.Start
		}
	}
	for _, a := range atts {
		if !a.OK {
			continue
		}
		name, ok := byUUID[a.UUID]
		if !ok {
			s.violate("INV-1", "an attestation succeeded for %s, which is not in the fleet", a.UUID)
			continue
		}
		for _, e := range r.events {
			if e.Fault == "stop" && e.Target == name && a.Time.After(e.Start) && a.Time.Before(e.End) {
				s.violate("INV-1", "%s was attested at %s while it was stopped (%s to %s)", name, a.Time.Format("15:04:05"), e.Start.Format("15:04:05"), e.End.Format("15:04:05"))
			}
		}
		if !switchAt.IsZero() && a.Time.Sub(switchAt).Abs() > time.Minute {
			if want := map[bool]string{true: "file_pull", false: "config_push"}[a.Time.Before(switchAt)]; a.Mode != want {
				s.violate("INV-1", "an attestation at %s used %s, the server's mode was %s", a.Time.Format("15:04:05"), a.Mode, want)
			}
		}
	}
}

// challengeKeys counts the challenge keys left on the fleet.
func (r *soakRun) challengeKeys() int {
	out := r.l.ssh(r.t, r.s.server, `sudo lxc query "/1.0/instances?recursion=1&project=default"`)
	var list []struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		r.t.Fatalf("listing the instances: %v", err)
	}
	n := 0
	for _, i := range list {
		for k := range i.Config {
			if strings.HasPrefix(k, "user.spire.challenge.") {
				n++
			}
		}
	}
	return n
}

// checkFinal judges the fleet after the last fault and a quiet period:
// INV-7 (every agent attested again after the last fault's clean-up, an
// agent's SVID being as old as its TTL minus its remaining life) and INV-4
// (no challenge key left: the next config_push attestation sweeps them).
func (r *soakRun) checkFinal(s *soakSummary, lastEnd time.Time) {
	agents := r.l.fleetAgents(r.t, r.s.server, r.uuids)
	var late []string
	for _, n := range r.names {
		a, ok := agents[n]
		expires, _ := strconv.ParseInt(a.Expires, 10, 64)
		if !ok || time.Unix(expires, 0).Add(-r.s.ttl).Before(lastEnd) {
			late = append(late, n)
		}
	}
	if len(late) > 0 {
		s.violate("INV-7", "%d agents were not attested again after the last fault ended at %s: %v", len(late), lastEnd.Format("15:04:05"), late)
	}
	if s.LeftoverKey = r.challengeKeys(); s.LeftoverKey > 0 {
		s.violate("INV-4", "%d challenge keys are left on the fleet after the quiet period", s.LeftoverKey)
	}
}

// checkDrift is INV-6: the processes' memory grew by over 50% between the
// first sample after the warm-up and the last. A finding.
func (r *soakRun) checkDrift(s *soakSummary) {
	if len(r.samples) < 8 {
		return
	}
	first, last := r.samples[5], r.samples[len(r.samples)-1]
	for _, c := range []struct {
		name        string
		first, last int
	}{{"SPIRE Server", first.ServerKiB, last.ServerKiB}, {"the server plugin", first.PluginKiB, last.PluginKiB}, {"LXD", first.LXDKiB, last.LXDKiB}} {
		if c.first > 0 && float64(c.last) > 1.5*float64(c.first) {
			s.find("INV-6: the memory of %s grew from %d to %d MiB", c.name, c.first/1024, c.last/1024)
		}
	}
}

// assess judges the soak.
func (r *soakRun) assess(start time.Time) *soakSummary {
	elapsed := time.Since(start)
	p := r.l.plugins(r.t)
	s := &soakSummary{
		Settings: map[string]string{"server": r.s.server, "fleet": strconv.Itoa(r.s.fleet), "duration": r.s.duration.String(), "fault_every": r.s.every.String(),
			"agent_ttl": r.s.ttl.String(), "attestation_timeout": r.s.attest.String(), "seed": strconv.FormatUint(r.s.seed, 10)},
		Environment: fmt.Sprintf("SPIRE %s, plugins %s, %s LXD %s", p.Spire.Version, p.Plugins.Version, r.s.server, r.l.env.Servers[r.s.server].Version),
		Events:      r.events, Samples: r.samples,
	}
	atts := r.journal(start)
	s.measures(atts, elapsed)
	r.checkSafety(s, atts)
	lastEnd := start
	for _, e := range r.events {
		lastEnd = e.End
	}
	r.checkFinal(s, lastEnd)
	r.checkDrift(s)
	for _, e := range r.events {
		if strings.Contains(e.Note, "NOT recovered") {
			s.find("the server plugin killed at %s: %s", e.Start.Format("15:04:05"), e.Note)
		}
	}
	if s.Refused["rate_limited"] > 0 {
		s.find("%d attestations were refused as rate_limited by the plugin's own limits", s.Refused["rate_limited"])
	}
	return s
}

// markdown renders the summary.
func (s *soakSummary) markdown(w io.Writer) error {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) } //nolint:errcheck // a buffered file
	p("# Soak with real agents\n\n%s\n\nSettings: %v\n\n", s.Environment, s.Settings)
	p("## Attestations\n\n| mode | succeeded | duration p50 / p95 / p99 / max (ms) | proof p50 / p95 / p99 / max (ms) |\n|---|---|---|---|\n")
	for m, n := range s.Succeeded {
		d, pr := s.Duration[m], s.Proof[m]
		p("| %s | %d | %d / %d / %d / %d | %d / %d / %d / %d |\n", m, n, d.P50, d.P95, d.P99, d.Max, pr.P50, pr.P95, pr.P99, pr.Max)
	}
	p("\nThroughput: %.2f attestations per second. Refused, by reason: %v\n\n## Invariants\n\n", s.PerSecond, s.Refused)
	if len(s.Violations) == 0 {
		p("INV-1, INV-4 and INV-7 held. Challenge keys left: %d.\n", s.LeftoverKey)
	}
	for _, v := range s.Violations {
		p("- **VIOLATION** %s\n", v)
	}
	p("\n## Findings\n\n")
	for _, f := range s.Findings {
		p("- %s\n", f)
	}
	p("\n## Faults\n\n| fault | target | start | end | proof mode | note |\n|---|---|---|---|---|---|\n")
	for _, e := range s.Events {
		p("| %s | %s | %s | %s | %s | %s |\n", e.Fault, e.Target, e.Start.Format("15:04:05"), e.End.Format("15:04:05"), e.Mode, e.Note)
	}
	p("\n## Memory (MiB)\n\n| at | SPIRE Server | server plugin | LXD | agents attested |\n|---|---|---|---|---|\n")
	for _, m := range s.Samples {
		p("| %s | %d | %d | %d | %d |\n", m.At.Format("15:04:05"), m.ServerKiB/1024, m.PluginKiB/1024, m.LXDKiB/1024, m.Agents)
	}
	return nil
}

// TestST7Soak: ST-7, the soak with real agents (STR-50 to STR-53): a fleet
// of real SPIRE Agents with the agent plugin attests continuously, their
// SVIDs short-lived, while faults are injected; the first half runs
// file_pull, the second config_push. The session passes when INV-1, INV-4
// and INV-7 hold. LAB_SOAK_* set its size (see loadSoakSettings).
func TestST7Soak(t *testing.T) {
	l := requirePlugins(t)
	s := loadSoakSettings(t)
	uuids := l.soakSetup(t, s)
	r := newSoakRun(t, l, s, uuids)
	start := time.Now().UTC()
	r.run()
	sum := r.assess(start)
	name := fmt.Sprintf("%s-st-7-%s-summary", start.Format("20060102T150405Z"), s.server)
	l.writeRun(t, name, len(sum.Violations) == 0, sum.markdown, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(sum)
	}, func(w io.Writer) error {
		for _, e := range sum.Events {
			if err := json.NewEncoder(w).Encode(e); err != nil {
				return err
			}
		}
		return nil
	})
	for _, v := range sum.Violations {
		t.Errorf("%s", v)
	}
}

// TestParseAttest reads the plugin's two attestation lines as SPIRE
// Server logs them, and ignores any other.
func TestParseAttest(t *testing.T) {
	ok, found := parseAttest(`time="2026-10-23T11:41:12Z" level=info msg="attestation succeeded" correlation_id=ad14abe5 duration_ms=119 external=true inflight=1 instance_uuid=6ff15e8c-8847-47d4-9769-939ae825988e outcome=success plugin_name=lxd_instance project=default proof_mode=file_pull proof_ms=39 reason_code=ok selectors="[server:lxd5 type:container]"`)
	if !found || !ok.OK || ok.UUID != "6ff15e8c-8847-47d4-9769-939ae825988e" || ok.Mode != "file_pull" || ok.DurationMS != 119 || ok.ProofMS != 39 || ok.Time.Second() != 12 {
		t.Errorf("success: %+v %v", ok, found)
	}
	refused, found := parseAttest(`time="2026-10-23T11:41:13Z" level=error msg="attestation refused" error="rate_limited: the claim is cooling down after a failure" outcome=failure reason_code=rate_limited`)
	if !found || refused.OK || refused.Reason != "rate_limited" {
		t.Errorf("refusal: %+v %v", refused, found)
	}
	for _, line := range []string{"", `time="2026-10-23T11:41:13Z" msg="something else"`, `msg="attestation succeeded" no time`} {
		if _, found := parseAttest(line); found {
			t.Errorf("%q parsed", line)
		}
	}
}

// TestLatencies: percentiles of milliseconds, the empty set included.
func TestLatencies(t *testing.T) {
	v := make([]int64, 100)
	for i := range v {
		v[i] = int64(100 - i)
	}
	if l := latencies(v); l.N != 100 || l.P50 != 51 || l.P95 != 96 || l.P99 != 100 || l.Max != 100 {
		t.Errorf("%+v", l)
	}
	if l := latencies(nil); l.N != 0 {
		t.Errorf("%+v", l)
	}
}

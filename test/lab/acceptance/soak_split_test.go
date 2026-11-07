//go:build lab

package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ST-8, the split soak (lab stress spec, STR-65): ST-7's fleet of real agents
// on two servers at once, faults on one server at a time, and a server
// plugin built with the race detector, sampled for leaks (INV-9, INV-10).

// splitPrefixes name each server's fleet: the same name on two servers would
// be ambiguous (attestor FR-S3).
var splitPrefixes = []string{"a", "b"}

// globalFaults stop SPIRE Server or the plugin itself, for every server.
var globalFaults = []string{"spire-restart", "plugin-kill"}

// isolationFaults take a server down or hang it: the other server's agents
// must keep attesting meanwhile (INV-9).
var isolationFaults = []string{"partition", "daemon", "hang"}

// debugSample is a sample of the debug server plugin (INV-10).
type debugSample struct {
	At         time.Time `json:"at"`
	PID        int       `json:"pid"`
	Goroutines int       `json:"goroutines"`
	HeapInuse  int64     `json:"heap_inuse_bytes"`
	RSSKiB     int       `json:"rss_kib"`
}

// splitRun is ST-8: one soakRun per server, driven together.
type splitRun struct {
	l       *lab
	t       *testing.T
	runs    []*soakRun
	rng     *rand.Rand
	debug   bool
	profDir string
	phaseA  time.Duration
	phaseB  time.Duration

	mu      sync.Mutex
	samples []debugSample
	races   map[string]bool
	phases  []soakEvent
}

// parseDebug reads the sampling script's output: pid=N, the plugin's
// resident memory, the goroutine profile's first line and the heap's
// HeapInuse line.
func parseDebug(out string) (debugSample, bool) {
	var d debugSample
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "pid="):
			d.PID = atoi(strings.TrimPrefix(line, "pid="))
		case strings.HasPrefix(line, "rss="):
			d.RSSKiB = atoi(strings.TrimPrefix(line, "rss="))
		case strings.HasPrefix(line, "goroutine profile: total "):
			d.Goroutines = atoi(strings.TrimPrefix(line, "goroutine profile: total "))
		case strings.HasPrefix(line, "# HeapInuse = "):
			d.HeapInuse, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "# HeapInuse = ")), 10, 64)
		}
	}
	return d, d.PID > 0 && d.Goroutines > 0
}

const debugScript = `p=$(pgrep -x lxd-server-plugin | head -n1); echo pid=$p; echo rss=$(ps -o rss= -p "$p")
curl -s --max-time 10 'http://127.0.0.1:6060/debug/pprof/goroutine?debug=1' | head -n1
curl -s --max-time 10 'http://127.0.0.1:6060/debug/pprof/heap?debug=1' | grep -m1 '^# HeapInuse = '`

func (sr *splitRun) spire(d time.Duration, command string) (string, error) {
	return sr.runs[0].spire(d, command)
}

// sampleDebug records the plugin's goroutines, heap and memory, and any
// new race report.
func (sr *splitRun) sampleDebug() {
	if !sr.debug {
		return
	}
	out, _ := sr.spire(40*time.Second, debugScript)
	if d, ok := parseDebug(out); ok {
		d.At = time.Now().UTC()
		sr.mu.Lock()
		sr.samples = append(sr.samples, d)
		sr.mu.Unlock()
	}
	files, _ := sr.spire(20*time.Second, "ls /var/lib/spire/server/ | grep '^race\\.' ; true")
	for _, f := range strings.Fields(files) {
		sr.mu.Lock()
		if !sr.races[f] {
			sr.races[f] = true
			sr.t.Logf("a race report appeared: %s", f)
		}
		sr.mu.Unlock()
	}
}

// profile keeps a 30-second CPU profile and a heap profile of the plugin in
// the working evidence (STR-65).
func (sr *splitRun) profile() {
	if !sr.debug {
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for kind, url := range map[string]string{"cpu": "profile?seconds=30", "heap": "heap"} {
		out, err := sr.spire(90*time.Second, fmt.Sprintf("curl -s --max-time 60 -o /tmp/st8.pb.gz 'http://127.0.0.1:6060/debug/pprof/%s' && base64 -w0 /tmp/st8.pb.gz", url))
		if err != nil {
			sr.t.Logf("profile %s: %v", kind, err)
			continue
		}
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
		if err != nil || len(data) == 0 {
			sr.t.Logf("profile %s: %d bytes, %v", kind, len(data), err)
			continue
		}
		if err := os.WriteFile(filepath.Join(sr.profDir, kind+"-"+stamp+".pb.gz"), data, 0o600); err != nil {
			sr.t.Logf("profile %s: %v", kind, err)
		}
	}
}

// switchMode changes the proof mode once, for every server's run.
func (sr *splitRun) switchMode(mode string) {
	sr.runs[0].switchMode(mode)
	e := sr.runs[0].events[len(sr.runs[0].events)-1]
	for _, r := range sr.runs[1:] {
		r.mu.Lock()
		r.mode = mode
		r.events = append(r.events, e)
		r.mu.Unlock()
	}
}

// phase runs one phase: faults on one server at a time, every s.every on
// average, and SPIRE Server and plugin faults only when global is set; the
// proof mode switches to half when it is set, halfway through.
func (sr *splitRun) phase(name string, d time.Duration, global bool, half string, soakStart time.Time) {
	start := time.Now()
	end, middle := start.Add(d), start.Add(d/2)
	allow := func(id string) bool { return global || !slices.Contains(globalFaults, id) }
	for _, r := range sr.runs {
		r.allow = allow
	}
	every := sr.runs[0].s.every
	next := start.Add(2 * time.Minute)
	nextSample, nextDebug, nextProfile := start, start, start.Add(10*time.Minute)
	switched := half == ""
	for time.Now().Before(end) {
		switch now := time.Now(); {
		case now.After(nextSample):
			for _, r := range sr.runs {
				r.sample()
			}
			nextSample = now.Add(time.Minute)
		case now.After(nextDebug):
			sr.sampleDebug()
			nextDebug = now.Add(5 * time.Minute)
		case now.After(nextProfile):
			sr.profile()
			nextProfile = now.Add(30 * time.Minute)
		case !switched && now.After(middle):
			sr.switchMode(half)
			switched = true
		case now.After(next):
			r := sr.runs[sr.rng.IntN(len(sr.runs))]
			r.inject(r.pick(now.Sub(soakStart)))
			next = time.Now().Add(time.Duration(float64(every) * (0.5 + sr.rng.Float64())))
		default:
			time.Sleep(5 * time.Second)
		}
	}
	sr.phases = append(sr.phases, soakEvent{Fault: "phase " + name, Start: start.UTC(), End: time.Now().UTC()})
	sr.t.Logf("phase %s over after %s", name, time.Since(start).Round(time.Second))
}

// run drives ST-8: A1 (file_pull) and A2 (config_push) with faults on the
// servers only, so that one plugin process lives through each; B with every
// fault, the mode switching halfway; then ST-7's quiet period.
func (sr *splitRun) run() {
	start := time.Now()
	sr.phase("A1", sr.phaseA, false, "", start)
	sr.switchMode("config_push")
	sr.phase("A2", sr.phaseA, false, "", start)
	sr.switchMode("file_pull")
	sr.phase("B", sr.phaseB, true, "config_push", start)
	quiet := sr.runs[0].s.ttl/2 + 150*time.Second
	sr.t.Logf("faults over after %s; %s of quiet", time.Since(start).Round(time.Second), quiet)
	for end := time.Now().Add(quiet); time.Now().Before(end); time.Sleep(time.Minute) {
		for _, r := range sr.runs {
			r.sample()
		}
		sr.sampleDebug()
	}
	sr.sampleDebug()
}

// median of ints; 0 for none.
func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

// leaks is INV-10: for each plugin process sampled for at least 60 minutes,
// the medians of its last 30 minutes against those of its first 30 minutes
// after a 15-minute warm-up.
func leaks(samples []debugSample) []string {
	byPID := map[int][]debugSample{}
	var pids []int
	for _, d := range samples {
		if _, ok := byPID[d.PID]; !ok {
			pids = append(pids, d.PID)
		}
		byPID[d.PID] = append(byPID[d.PID], d)
	}
	var out []string
	for _, pid := range pids {
		g := byPID[pid]
		first, last := g[0].At, g[len(g)-1].At
		if last.Sub(first) < 60*time.Minute {
			continue
		}
		var gFirst, gLast, hFirst, hLast []int64
		for _, d := range g {
			switch {
			case !d.At.Before(first.Add(15*time.Minute)) && d.At.Before(first.Add(45*time.Minute)):
				gFirst, hFirst = append(gFirst, int64(d.Goroutines)), append(hFirst, d.HeapInuse)
			case !d.At.Before(last.Add(-30 * time.Minute)):
				gLast, hLast = append(gLast, int64(d.Goroutines)), append(hLast, d.HeapInuse)
			}
		}
		if len(gFirst) == 0 || len(gLast) == 0 {
			continue
		}
		gf, gl, hf, hl := median(gFirst), median(gLast), median(hFirst), median(hLast)
		if float64(gl) > 1.2*float64(gf)+20 {
			out = append(out, fmt.Sprintf("plugin process %d: goroutines grew from %d to %d over %s", pid, gf, gl, last.Sub(first).Round(time.Minute)))
		}
		if float64(hl) > 1.5*float64(hf)+16<<20 {
			out = append(out, fmt.Sprintf("plugin process %d: heap in use grew from %d to %d MiB over %s", pid, hf>>20, hl>>20, last.Sub(first).Round(time.Minute)))
		}
	}
	return out
}

// isolation is INV-9: during each fault of at least 20 s that takes one
// server down or hangs it, at least one agent of each other server attests.
// It also returns the durations of those attestations, by server.
func isolation(runs []*soakRun, atts []soakAttest) (violations []string, during map[string][]int64) {
	during = map[string][]int64{}
	owner := map[string]string{}
	for _, r := range runs {
		for _, u := range r.uuids {
			owner[u] = r.s.server
		}
	}
	for _, x := range runs {
		for _, e := range x.events {
			if !slices.Contains(isolationFaults, e.Fault) || e.End.Sub(e.Start) < 20*time.Second {
				continue
			}
			for _, y := range runs {
				if y == x {
					continue
				}
				n := 0
				for _, a := range atts {
					if a.OK && owner[a.UUID] == y.s.server && !a.Time.Before(e.Start) && !a.Time.After(e.End) {
						n++
						during[y.s.server] = append(during[y.s.server], a.DurationMS)
					}
				}
				if n == 0 {
					violations = append(violations, fmt.Sprintf("INV-9: during %s on %s (%s to %s), no agent of %s attested",
						e.Fault, x.s.server, e.Start.Format("15:04:05"), e.End.Format("15:04:05"), y.s.server))
				}
			}
		}
	}
	return violations, during
}

// splitSummary is ST-8's result.
type splitSummary struct {
	Settings    map[string]string       `json:"settings"`
	Environment string                  `json:"environment"`
	Refused     map[string]int          `json:"refused_by_reason"`
	Servers     map[string]*soakSummary `json:"servers"`
	During      map[string]soakLatency  `json:"duration_ms_during_other_server_faults"`
	Skips       map[string]int          `json:"skip_warnings_by_server"`
	Debug       []debugSample           `json:"plugin_samples"`
	Races       []string                `json:"race_reports"`
	Phases      []soakEvent             `json:"phases"`
	Violations  []string                `json:"violations"`
	Findings    []string                `json:"findings"`
}

// skips counts the plugin's skip warnings by server.
func (sr *splitRun) skips(start time.Time) map[string]int {
	out, _ := sr.spire(2*time.Minute, fmt.Sprintf(`journalctl -u spire-server --since "@%d" --no-pager -o cat | grep 'skipping an LXD server' | grep -o 'server_id=[A-Za-z0-9._-]*'; true`, start.Unix()))
	m := map[string]int{}
	for _, f := range strings.Fields(out) {
		m[strings.TrimPrefix(f, "server_id=")]++
	}
	return m
}

func (sr *splitRun) assess(start time.Time) *splitSummary {
	base := sr.runs[0].s
	p := sr.l.plugins(sr.t)
	sum := &splitSummary{
		Settings: map[string]string{"fleet_per_server": strconv.Itoa(base.fleet), "phase_a": sr.phaseA.String(), "phase_b": sr.phaseB.String(),
			"fault_every": base.every.String(), "agent_ttl": base.ttl.String(), "attestation_timeout": base.attest.String(),
			"seed": strconv.FormatUint(base.seed, 10), "debug_plugin": strconv.FormatBool(sr.debug)},
		Servers: map[string]*soakSummary{}, Refused: map[string]int{}, During: map[string]soakLatency{},
		Debug: sr.samples, Phases: sr.phases,
	}
	env := []string{fmt.Sprintf("SPIRE %s, plugins %s", p.Spire.Version, p.Plugins.Version)}
	atts := sr.runs[0].journal(start)
	known := map[string]bool{}
	for _, r := range sr.runs {
		env = append(env, fmt.Sprintf("%s LXD %s", r.s.server, sr.l.env.Servers[r.s.server].Version))
		var mine []soakAttest
		for _, a := range atts {
			if a.OK && slices.Contains(mapValues(r.uuids), a.UUID) {
				mine = append(mine, a)
			}
		}
		for _, u := range r.uuids {
			known[u] = true
		}
		sum.Servers[r.s.server] = r.assessAtts(start, mine)
		for _, v := range sum.Servers[r.s.server].Violations {
			sum.Violations = append(sum.Violations, r.s.server+": "+v)
		}
	}
	sum.Environment = strings.Join(env, ", ")
	for _, a := range atts {
		switch {
		case !a.OK:
			sum.Refused[a.Reason]++
		case !known[a.UUID]:
			sum.Violations = append(sum.Violations, fmt.Sprintf("INV-1: an attestation succeeded for %s, which is in no fleet", a.UUID))
		}
	}
	inv9, during := isolation(sr.runs, atts)
	sum.Violations = append(sum.Violations, inv9...)
	for server, v := range during {
		sum.During[server] = latencies(v)
	}
	for _, l := range leaks(sr.samples) {
		sum.Violations = append(sum.Violations, "INV-10: "+l)
	}
	for f := range sr.races {
		report, _ := sr.spire(30*time.Second, "head -n 60 /var/lib/spire/server/"+f)
		sum.Races = append(sum.Races, f+":\n"+report)
		sum.Violations = append(sum.Violations, "a data race was reported: "+f)
	}
	slices.Sort(sum.Races)
	sum.Skips = sr.skips(start)
	if sum.Refused["rate_limited"] > 0 {
		sum.Findings = append(sum.Findings, fmt.Sprintf("%d attestations were refused as rate_limited by the plugin's own limits", sum.Refused["rate_limited"]))
	}
	if sr.debug && len(sr.samples) == 0 {
		sum.Violations = append(sum.Violations, "INV-10: the debug plugin was never sampled")
	}
	return sum
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (s *splitSummary) markdown(w io.Writer) error {
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) } //nolint:errcheck // a buffered file
	p("# Split soak (ST-8)\n\n%s\n\nSettings: %v\n\n## Invariants\n\n", s.Environment, s.Settings)
	if len(s.Violations) == 0 {
		p("INV-1, INV-4, INV-7, INV-9 and INV-10 held; no data race.\n")
	}
	for _, v := range s.Violations {
		p("- **VIOLATION** %s\n", v)
	}
	p("\n## Findings\n\n")
	for _, f := range s.Findings {
		p("- %s\n", f)
	}
	p("\nRefused, by reason: %v\n\nSkip warnings, by server skipped: %v\n\n", s.Refused, s.Skips)
	p("## Attestations of one server during the other's faults\n\n| server | n | duration p50 / p95 / p99 / max (ms) |\n|---|---|---|\n")
	for server, l := range s.During {
		p("| %s | %d | %d / %d / %d / %d |\n", server, l.N, l.P50, l.P95, l.P99, l.Max)
	}
	p("\n## Phases\n\n| phase | start | end |\n|---|---|---|\n")
	for _, e := range s.Phases {
		p("| %s | %s | %s |\n", e.Fault, e.Start.Format("15:04:05"), e.End.Format("15:04:05"))
	}
	p("\n## Server plugin (debug build)\n\n| at | pid | goroutines | heap in use (MiB) | RSS (MiB) |\n|---|---|---|---|---|\n")
	for _, d := range s.Debug {
		p("| %s | %d | %d | %d | %d |\n", d.At.Format("15:04:05"), d.PID, d.Goroutines, d.HeapInuse>>20, d.RSSKiB/1024)
	}
	for _, r := range s.Races {
		p("\n```\n%s\n```\n", r)
	}
	servers := make([]string, 0, len(s.Servers))
	for server := range s.Servers {
		servers = append(servers, server)
	}
	slices.Sort(servers)
	for _, server := range servers {
		p("\n---\n\n## %s\n\n", server)
		if err := s.Servers[server].markdown(w); err != nil {
			return err
		}
	}
	return nil
}

// TestST8SplitSoak: ST-8 (STR-65). It runs only with LAB_SPLIT=yes: it lasts
// hours. LAB_SOAK_SERVERS, LAB_SOAK_FLEET (per server), LAB_SPLIT_A,
// LAB_SPLIT_B and the other LAB_SOAK_* settings size it; LAB_SPLIT_DEBUG=no
// runs it without the debug plugin.
func TestST8SplitSoak(t *testing.T) {
	if os.Getenv("LAB_SPLIT") != "yes" {
		t.Skip("ST-8 lasts hours: LAB_SPLIT=yes runs it")
	}
	l := requirePlugins(t)
	base := loadSoakSettings(t)
	servers := strings.Fields(setting("LAB_SOAK_SERVERS", "lxd5 lxd6"))
	if len(servers) != len(splitPrefixes) {
		t.Fatalf("LAB_SOAK_SERVERS %v: two servers", servers)
	}
	sr := &splitRun{l: l, t: t, rng: rand.New(rand.NewPCG(base.seed, base.seed^1)), races: map[string]bool{},
		debug: setting("LAB_SPLIT_DEBUG", "yes") == "yes"}
	var err error
	if sr.phaseA, err = time.ParseDuration(setting("LAB_SPLIT_A", "90m")); err != nil {
		t.Fatal(err)
	}
	if sr.phaseB, err = time.ParseDuration(setting("LAB_SPLIT_B", "150m")); err != nil {
		t.Fatal(err)
	}
	base.duration = 2*sr.phaseA + sr.phaseB
	for i, server := range servers {
		s := base
		s.server, s.prefix = server, splitPrefixes[i]
		s.seed = base.seed + uint64(i)
		uuids := l.soakSetup(t, s)
		sr.runs = append(sr.runs, newSoakRun(t, l, s, uuids))
	}
	if sr.debug {
		out, _ := sr.spire(40*time.Second, debugScript)
		if _, ok := parseDebug(out); !ok {
			t.Fatalf("the debug server plugin does not answer (LAB_SERVER_PLUGIN_DEBUG=yes lab.sh deploy, or LAB_SPLIT_DEBUG=no): %q", out)
		}
	}
	start := time.Now().UTC()
	name := fmt.Sprintf("%s-st-8-summary", start.Format("20060102T150405Z"))
	sr.profDir = filepath.Join(l.evidence, start.Format("20060102T150405Z")+"-st-8-profiles")
	if err := os.MkdirAll(sr.profDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sr.run()
	sum := sr.assess(start)
	l.writeRun(t, name, len(sum.Violations) == 0, sum.markdown, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(sum)
	}, func(w io.Writer) error {
		for _, r := range sr.runs {
			for _, e := range r.events {
				e.Target = r.s.server + "/" + e.Target
				if err := json.NewEncoder(w).Encode(e); err != nil {
					return err
				}
			}
		}
		return nil
	})
	t.Logf("profiles in %s", sr.profDir)
	for _, v := range sum.Violations {
		t.Errorf("%s", v)
	}
}

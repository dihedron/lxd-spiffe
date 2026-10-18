//go:build lab

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/test/lab/stress"
)

// The stress sessions (stress spec, lxd-stress.md): a fleet of Alpine
// containers per server plus the baseline, driven by test/lab/stress.

// stressImage is the fleet's image: Alpine without cloud-init, small.
const stressImage = "images:alpine/3.24"

// stressSettings are the sessions' settings (STR-62).
type stressSettings struct {
	fleet      int
	iterations int
	workers    int
	faultRate  float64
	seed       uint64
}

func loadStressSettings(t *testing.T) stressSettings {
	t.Helper()
	atoi := func(name string, def int) int {
		n, err := strconv.Atoi(setting(name, strconv.Itoa(def)))
		if err != nil || n < 0 {
			t.Fatalf("%s: not a count", name)
		}
		return n
	}
	rate, err := strconv.ParseFloat(setting("LAB_STRESS_FAULT_RATE", "0.2"), 64)
	if err != nil || rate < 0 || rate > 1 {
		t.Fatal("LAB_STRESS_FAULT_RATE: not a rate between 0 and 1")
	}
	seed, err := strconv.ParseUint(setting("LAB_STRESS_SEED", strconv.FormatInt(time.Now().UnixNano(), 10)), 10, 64)
	if err != nil {
		t.Fatal("LAB_STRESS_SEED: not a number")
	}
	return stressSettings{
		fleet:   atoi("LAB_STRESS_FLEET", 35), // LXD allows 99 permissions per group: see STR-01 iterations: atoi("LAB_STRESS_ITERATIONS", 500),
		workers: atoi("LAB_STRESS_WORKERS", 10), faultRate: rate, seed: seed,
	}
}

func (s stressSettings) record() map[string]string {
	return map[string]string{
		"fleet": strconv.Itoa(s.fleet), "iterations": strconv.Itoa(s.iterations), "workers": strconv.Itoa(s.workers),
		"fault_rate": strconv.FormatFloat(s.faultRate, 'f', -1, 64), "seed": strconv.FormatUint(s.seed, 10),
	}
}

// fleetNames are the fleet's instance names on a server.
func fleetNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("s-%03d", i+1)
	}
	return names
}

// stressBaseline are the baseline instances that join the fleet (STR-01).
var stressBaseline = []string{"c-unpriv", "c-priv", "c-iso", "c-raw", "v-ubuntu", "v-debian", "v-alpine"}

// fleetGrants are the lab groups' entitlements on each fleet instance, as
// lab_grant gives them on the baseline (project can_view is there already).
var fleetGrants = []struct{ group, entitlements string }{
	{"lab-read", "can_view"}, {"lab-files", "can_view can_access_files"}, {"lab-edit", "can_view can_edit"},
}

// fleetSetup builds the fleet on server (STR-01) and deletes it when the
// test ends, and first if a previous session left one (STR-02). It also
// lifts the VM's sshd limits, so that concurrent guest halves are not
// refused by MaxStartups or MaxSessions (gone at the next lab.sh reset).
func (l *lab) fleetSetup(t *testing.T, server string, names []string) {
	t.Helper()
	var td strings.Builder
	td.WriteString("set +e\n")
	for _, n := range names {
		for _, g := range fleetGrants {
			for _, e := range strings.Fields(g.entitlements) {
				fmt.Fprintf(&td, "sudo lxc auth group permission remove %s instance %s %s project=default 2>/dev/null\n", g.group, n, e)
			}
		}
	}
	td.WriteString(`for i in $(sudo lxc list -c n -f csv 's-[0-9][0-9][0-9]$'); do sudo lxc delete --force "$i"; done; true` + "\n")
	teardown := td.String()
	l.ssh(t, server, teardown)
	t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort
	l.ssh(t, server, `printf 'MaxStartups 200:30:400\nMaxSessions 200\n' | sudo tee /etc/ssh/sshd_config.d/90-lab-stress.conf >/dev/null && sudo systemctl reload ssh`)

	start := time.Now()
	list := strings.Join(names, " ")
	// eight launches at a time; LXD occasionally fails one under that load
	// (a zfs clone "bad file descriptor" was seen on 6.9), so every instance
	// that is not running is launched again alone, up to three times
	launch := fmt.Sprintf(`printf '%%s\n' %s | xargs -P 8 -I{} sudo lxc launch %s {} -q 2>/dev/null; `+
		`for n in %s; do for try in 1 2 3; do `+
		`[ "$(sudo lxc list -c ns -f csv "^$n$")" = "$n,RUNNING" ] && break; `+
		`echo "relaunching $n (try $try)"; sudo lxc delete --force "$n" 2>/dev/null; sudo lxc launch %s "$n" -q; sleep 2; done; `+
		`[ "$(sudo lxc list -c ns -f csv "^$n$")" = "$n,RUNNING" ] || { echo "$n does not run" >&2; exit 1; }; done`,
		list, stressImage, list, stressImage)
	if out := l.ssh(t, server, launch); strings.TrimSpace(out) != "" {
		t.Logf("%s: %s", server, strings.TrimSpace(out))
	}
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, n := range names {
		fmt.Fprintf(&script, "sudo lxc file push lxd-probe %s/usr/local/bin/lxd-probe --uid 0 --gid 0 --mode 0755\n", n)
		fmt.Fprintf(&script, "sudo lxc exec %s -- sh -c 'mkdir -p /var/lib/lxd-probe/evidence && chmod 0700 /var/lib/lxd-probe/evidence'\n", n)
		for _, g := range fleetGrants {
			for _, e := range strings.Fields(g.entitlements) {
				fmt.Fprintf(&script, "sudo lxc auth group permission add %s instance %s %s project=default\n", g.group, n, e)
			}
		}
	}
	l.ssh(t, server, script.String())
	t.Logf("%s: fleet of %d built in %s", server, len(names), time.Since(start).Round(time.Second))
}

// stressRun is one stress session's run on the lab: where its records go
// and how an iteration is performed.
type stressRun struct {
	l       *lab
	session session
	label   string
}

// iterate runs one item: the rehearsal, then, for file_pull, the guest's
// removal of the proof file. Leftovers (INV-4): for file_pull, a proof
// file the removal could not remove after a pass; for config_push, a
// cleanup the rehearsal reports as failed (cleanup.result).
func (sr *stressRun) iterate(ctx context.Context, it stress.Item) (r stress.Result) {
	l := sr.l
	r = stress.Result{Item: it, Label: sr.label, Start: time.Now()}
	defer func() { r.End = time.Now() }() // r is the named result: the deferred write reaches the caller
	cell := it.Target.Server + "-" + it.Target.Instance
	if it.Fault != "" {
		cell += "-" + it.Fault
	}
	tags := []string{"--gate", "S", "--cell", cell}
	identity, sargs, gargs := "files", append([]string{"--allow-privileged"}, tags...), append(append([]string{}, spireWrite...), tags...)
	if it.Mode == stress.ConfigPush {
		identity = "edit"
		sargs = append(sargs, "--allow-write", "--lab", "--allow-spire-keys")
		gargs = tags
	}
	reh, err := l.rehearseE(ctx, sr.session, it.Target.Server, identity, "default", it.Target.Instance, it.Mode, sargs, gargs)
	if err != nil {
		r.Outcome, r.Detail = stress.Error, err.Error()
		return r
	}
	r.Steps = stepTimings(reh.server)
	result := reh.server.result()
	switch {
	case result == "passed":
		r.Outcome = stress.Pass
	case strings.HasPrefix(result, "failed at "):
		r.Outcome = stress.Fail
		r.FailedStep, _, _ = strings.Cut(strings.TrimPrefix(result, "failed at "), ",")
	default:
		r.Outcome, r.Detail = stress.Error, "no rehearsal result: "+reh.server.String()
	}
	if it.Mode == stress.FilePull && reh.challenge.ProofPath != "" && reh.guest != nil {
		rm, err := l.guestE(ctx, sr.session, it.Target.Server, "default", it.Target.Instance,
			append(append([]string{"guest", "file", "rm", reh.challenge.ProofPath}, spireWrite...), tags...)...)
		if (err != nil || rm.rec.Outcome != "ok") && r.Outcome == stress.Pass {
			r.Leftovers++
		}
	}
	if it.Mode == stress.ConfigPush {
		if v, ok := reh.server.observation("cleanup.result"); ok {
			r.Leftovers++
			r.Detail = fmt.Sprint(v)
		}
	}
	return r
}

// stepTimings reads the step.<name>.ms observations (PRS-54).
func stepTimings(r *run) map[string]int64 {
	out := map[string]int64{}
	if r == nil || r.rec == nil {
		return out
	}
	for _, o := range r.rec.Observations {
		name, ok := strings.CutPrefix(o.Key, "step.")
		if !ok {
			continue
		}
		if name, ok = strings.CutSuffix(name, ".ms"); ok {
			if v, ok := o.Value.(float64); ok {
				out[name] = int64(v)
			}
		}
	}
	return out
}

// stressTargets are the fleet and the baseline of every server.
func (l *lab) stressTargets(names []string) []stress.Target {
	var out []stress.Target
	for _, server := range l.servers() {
		for _, n := range append(append([]string{}, names...), stressBaseline...) {
			out = append(out, stress.Target{Server: server, Instance: n})
		}
	}
	return out
}

// buildFleets builds the fleet on every server, one after another (each
// server launches its instances eight at a time).
func (l *lab) buildFleets(t *testing.T, names []string) {
	t.Helper()
	for _, server := range l.servers() {
		l.fleetSetup(t, server, names)
	}
}

// writeSummary writes summary.md, summary.json and results.jsonl into
// dir, and with LAB_COLLECT=yes copies them to docs/validation/<name>/
// (STR-41, STR-42).
func (l *lab) writeSummary(t *testing.T, name string, sum stress.Summary, results []stress.Result) {
	t.Helper()
	dir := filepath.Join(l.evidence, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(file string, fn func(*os.File) error) {
		f, err := os.Create(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close() //nolint:errcheck // checked by the write
		if err := fn(f); err != nil {
			t.Fatal(err)
		}
	}
	write("summary.md", func(f *os.File) error { return sum.WriteMarkdown(f) })
	write("summary.json", func(f *os.File) error { return sum.WriteJSON(f) })
	write("results.jsonl", func(f *os.File) error { return stress.WriteResults(f, results) })
	md, _ := os.ReadFile(filepath.Join(dir, "summary.md"))
	t.Logf("summary in %s:\n%s", dir, md)
	if os.Getenv("LAB_COLLECT") == "yes" && !t.Failed() {
		dst := filepath.Join(l.repo, "docs", "validation", name)
		if err := copyTree(dir, dst, nil); err != nil {
			t.Fatalf("collecting the summary: %v", err)
		}
		t.Logf("collected: %s", dst)
	}
}

// stressBounds are the invariants' bounds: --timeout (30 s) plus 10 s,
// and 60 s to recover (INV-2, INV-3).
var stressBounds = stress.Bounds{Iteration: 40 * time.Second, Recovery: 60 * time.Second}

// eachInstanceOnce is every target in every mode, once.
func eachInstanceOnce(targets []stress.Target) []stress.Item {
	var items []stress.Item
	for _, mode := range []string{rehearse.FilePull, rehearse.ConfigPush} {
		for _, tg := range targets {
			items = append(items, stress.Item{N: len(items), Target: tg, Mode: mode})
		}
	}
	return items
}

// TestST1Fleet is ST-1: the fleet is built on every server, every fleet
// and baseline instance passes one rehearsal per mode, and the fleet is
// deleted at the end.
func TestST1Fleet(t *testing.T) {
	l := newLab(t)
	set := loadStressSettings(t)
	names := fleetNames(set.fleet)
	l.buildFleets(t, names)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	sr := &stressRun{l: l, session: session{evidenceDir: l.evidence, runID: stamp + "-st-1"}, label: "fleet check"}
	results := stress.Run(context.Background(), eachInstanceOnce(l.stressTargets(names)), set.workers, sr.iterate)
	sum := stress.Summarize(results, set.record(), stressBounds)
	l.writeSummary(t, stamp+"-st-1-summary", sum, results)
	for _, r := range results {
		if r.Outcome != stress.Pass {
			t.Errorf("%s/%s %s: %s %s %s", r.Target.Server, r.Target.Instance, r.Mode, r.Outcome, r.FailedStep, r.Detail)
		}
	}
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

// TestST2Load is ST-2: every instance in every mode at concurrency 1, 5,
// 10 and 20, without faults (STR-10); INV-2 and INV-4.
func TestST2Load(t *testing.T) {
	l := newLab(t)
	set := loadStressSettings(t)
	names := fleetNames(set.fleet)
	l.buildFleets(t, names)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var results []stress.Result
	for _, c := range []int{1, 5, 10, 20} {
		sr := &stressRun{l: l, session: session{evidenceDir: l.evidence, runID: stamp + "-st-2"}, label: fmt.Sprintf("concurrency %02d", c)}
		items := eachInstanceOnce(l.stressTargets(names))
		for i := range items {
			items[i].N += len(results)
		}
		step := stress.Run(context.Background(), items, c, sr.iterate)
		t.Logf("%s: %d iterations", sr.label, len(step))
		results = append(results, step...)
	}
	settings := set.record()
	settings["session"] = "ST-2"
	sum := stress.Summarize(results, settings, stressBounds)
	l.writeSummary(t, stamp+"-st-2-summary", sum, results)
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

// TestST5Burst is ST-5: the attestor's rate limit (SEC-09: 20/s, 16 in
// flight) as offered load for 60 s (STR-12); INV-2 and INV-4. The guest
// half's transport (SSH and lxc exec) may keep the lab below the offered
// rate: the summary reports the rate reached.
func TestST5Burst(t *testing.T) {
	l := newLab(t)
	set := loadStressSettings(t)
	names := fleetNames(set.fleet)
	l.buildFleets(t, names)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	sr := &stressRun{l: l, session: session{evidenceDir: l.evidence, runID: stamp + "-st-5"}, label: "burst 20/s"}
	plan := stress.Plan{Seed: set.seed, Iterations: 20 * 60, Modes: []string{rehearse.FilePull, rehearse.ConfigPush}, Targets: l.stressTargets(names)}
	tick := time.NewTicker(time.Second / 20)
	defer tick.Stop()
	paced := func(ctx context.Context, it stress.Item) stress.Result {
		select {
		case <-tick.C:
		case <-ctx.Done():
		}
		return sr.iterate(ctx, it)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	results := stress.Run(ctx, plan.Items(), 16, paced)
	settings := set.record()
	settings["session"], settings["offered"] = "ST-5", "20/s for 60 s, 16 in flight"
	sum := stress.Summarize(results, settings, stressBounds)
	l.writeSummary(t, stamp+"-st-5-summary", sum, results)
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

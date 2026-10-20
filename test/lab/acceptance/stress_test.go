//go:build lab

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
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
		// the fleet: its grants are sharded over groups, see STR-01
		fleet:      atoi("LAB_STRESS_FLEET", 100),
		iterations: atoi("LAB_STRESS_ITERATIONS", 500),
		workers:    atoi("LAB_STRESS_WORKERS", 10), faultRate: rate, seed: seed,
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

// fleetGrants are the lab identities' entitlements on each fleet instance,
// as lab_grant gives them on the baseline. The identity and its baseline
// group share the name.
var fleetGrants = []struct{ group, entitlements string }{
	{"lab-read", "can_view"}, {"lab-files", "can_view can_access_files"}, {"lab-edit", "can_view can_edit"},
}

// shardCapacity is the most instance grants a shard group holds: a group
// holds at most 99 permissions (gate L17), and project can_view is one.
const shardCapacity = 90

// shardScript gives each lab identity its fleet grants through shard
// groups <group>-s1, <group>-s2, …: each holds project can_view (first)
// and at most shardCapacity instance grants, and the identity is added to
// it, as the attestor's Least privilege prescribes (STR-01, gate L17).
func shardScript(names []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, g := range fleetGrants {
		ents := strings.Fields(g.entitlements)
		per := shardCapacity / len(ents)
		for k := 0; k*per < len(names); k++ {
			shard := fmt.Sprintf("%s-s%d", g.group, k+1)
			fmt.Fprintf(&b, "sudo lxc auth group create %s\nsudo lxc auth group permission add %s project default can_view\n", shard, shard)
			for _, n := range names[k*per : min((k+1)*per, len(names))] {
				for _, e := range ents {
					fmt.Fprintf(&b, "sudo lxc auth group permission add %s instance %s %s project=default\n", shard, n, e)
				}
			}
			fmt.Fprintf(&b, "sudo lxc auth identity group add tls/%s %s\n", g.group, shard)
		}
	}
	return b.String()
}

// fleetSetup builds the fleet on server (STR-01) and deletes it when the
// test ends, and first if a previous session left one (STR-02). It also
// lifts the VM's sshd limits, so that concurrent guest halves are not
// refused by MaxStartups or MaxSessions (gone at the next lab.sh reset).
func (l *lab) fleetSetup(t *testing.T, server string, names []string, members ...string) {
	t.Helper()
	var td strings.Builder
	td.WriteString("set +e\n")
	// the shard groups (their grants and memberships go with them), then
	// grants an unsharded session of chunk 16 put in the baseline groups
	td.WriteString(`for g in $(sudo lxc auth group list -f csv | cut -d, -f1 | grep -E '^lab-(read|files|edit)-s[0-9]+$'); do sudo lxc auth group delete "$g"; done` + "\n")
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
	// an instance volume LXD left behind blocks launching that name again
	// ("UNIQUE constraint failed"), deep in the launches below
	if orphans := l.orphanVolumes(t, server); len(orphans) > 0 {
		t.Fatalf("%s has instance volumes without their instance, left by an earlier session (a failed evacuation): %s; run lab.sh reset", server, strings.Join(orphans, " "))
	}
	l.ssh(t, server, `printf 'MaxStartups 200:30:400\nMaxSessions 200\n' | sudo tee /etc/ssh/sshd_config.d/90-lab-stress.conf >/dev/null && sudo systemctl reload ssh`)

	start := time.Now()
	// eight launches at a time; LXD occasionally fails one under that load
	// (a zfs clone "bad file descriptor" was seen on 6.9), so every instance
	// that is not running is launched again alone, up to three times
	// on a cluster the fleet is spread over the members, round robin
	var pairs strings.Builder
	for i, n := range names {
		target := "-"
		if len(members) > 0 {
			target = members[i%len(members)]
		}
		fmt.Fprintf(&pairs, "%s %s ", n, target)
	}
	launchOne := `n=$1; m=$2; if [ "$m" = - ]; then sudo lxc launch ` + stressImage + ` "$n" -q; else sudo lxc launch ` + stressImage + ` "$n" --target "$m" -q; fi`
	launch := fmt.Sprintf(`printf '%%s %%s\n' %s | xargs -P 8 -n 2 sh -c '%s' sh 2>/dev/null; `+
		`set -- %s; while [ $# -gt 0 ]; do n=$1; m=$2; shift 2; for try in 1 2 3; do `+
		`[ "$(sudo lxc list -c ns -f csv "^$n$")" = "$n,RUNNING" ] && break; `+
		`echo "relaunching $n (try $try)"; sudo lxc delete --force "$n" 2>/dev/null; sh -c '%s' sh "$n" "$m"; sleep 2; done; `+
		`[ "$(sudo lxc list -c ns -f csv "^$n$")" = "$n,RUNNING" ] || { echo "$n does not run" >&2; exit 1; }; done`,
		pairs.String(), launchOne, pairs.String(), launchOne)
	if out := l.ssh(t, server, launch); strings.TrimSpace(out) != "" {
		t.Logf("%s: %s", server, strings.TrimSpace(out))
	}
	// LXD's file transfer into a container occasionally breaks for one
	// container of a fresh fleet (a forkfile socket error was seen on 6.9,
	// lasting across quick retries, not reproducible on demand): each
	// install is tried again after restarting the container, then after
	// recreating it
	var script strings.Builder
	script.WriteString("set -e\ninstall() { for try in 1 2 3 4; do " +
		"sudo lxc file push lxd-probe \"$1\"/usr/local/bin/lxd-probe --uid 0 --gid 0 --mode 0755 && " +
		"sudo lxc exec \"$1\" -- sh -c 'mkdir -p /var/lib/lxd-probe/evidence && chmod 0700 /var/lib/lxd-probe/evidence' && return 0; " +
		"case $try in 1|2) echo \"install in $1 failed: restarting it\"; sudo lxc restart --force \"$1\";; " +
		"3) echo \"install in $1 failed: recreating it\"; sudo lxc delete --force \"$1\"; sudo lxc launch " + stressImage + " \"$1\" -q;; esac; " +
		"sleep 3; done; return 1; }\n")
	for _, n := range names {
		fmt.Fprintf(&script, "install %s\n", n)
	}
	script.WriteString(shardScript(names))
	if out := strings.TrimSpace(l.ssh(t, server, script.String())); out != "" {
		t.Logf("%s: %s", server, out) // the installs that needed a restart or a new container
	}
	t.Logf("%s: fleet of %d built in %s", server, len(names), time.Since(start).Round(time.Second))
}

// stressRun is one stress session's run on the lab: where its records go
// and how an iteration is performed.
type stressRun struct {
	l       *lab
	session session
	label   string
	// unchecked are the iterations whose leftovers could not be checked,
	// the instance out of reach under a fault: checked again at the end
	mu        sync.Mutex
	unchecked map[uncheckedKey]uncheckedLeftover
}

// uncheckedKey names one result: its item, target and start.
type uncheckedKey struct {
	n      int
	target stress.Target
	start  time.Time
}

// uncheckedLeftover is what the late check needs.
type uncheckedLeftover struct {
	it stress.Item
	c  rehearse.Challenge
}

// leftovers sets r's leftovers (INV-4), or, when the instance cannot be
// reached, keeps the check for recheckLeftovers: counted as none, it hid
// leftovers that the sweep then found (ST-4).
func (sr *stressRun) leftovers(r *stress.Result, it stress.Item, c rehearse.Challenge) {
	n, known := sr.checkLeftovers(it, c)
	r.Leftovers = n
	if known {
		return
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.unchecked == nil {
		sr.unchecked = map[uncheckedKey]uncheckedLeftover{}
	}
	sr.unchecked[uncheckedKey{it.N, it.Target, r.Start}] = uncheckedLeftover{it, c}
}

// recheckLeftovers checks again, on the lab the faults have left, the
// leftovers of the results whose check could not reach the instance.
func (sr *stressRun) recheckLeftovers(t *testing.T, results []stress.Result) {
	t.Helper()
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if len(sr.unchecked) == 0 {
		return
	}
	found, unknown := 0, 0
	for i := range results {
		u, ok := sr.unchecked[uncheckedKey{results[i].N, results[i].Target, results[i].Start}]
		if !ok {
			continue
		}
		n, known := sr.checkLeftovers(u.it, u.c)
		if !known {
			unknown++
			continue
		}
		results[i].Leftovers = n
		found += n
	}
	t.Logf("leftovers checked again after the session: %d checks, %d found, %d still unknown", len(sr.unchecked), found, unknown)
}

// iterate runs one item: the rehearsal, then, for file_pull, the guest's
// removal of the proof file. Leftovers (INV-4): for file_pull, a proof
// file the removal could not remove after a pass; for config_push, a
// cleanup the rehearsal reports as failed (cleanup.result).
func (sr *stressRun) iterate(ctx context.Context, it stress.Item) stress.Result {
	r, _ := sr.iterateHooked(ctx, it, rehearseHooks{}, nil)
	return r
}

// iterateHooked is iterate with a fault's hooks and extra guest flags; it
// also returns the challenge, for the faults' leftover checks.
func (sr *stressRun) iterateHooked(ctx context.Context, it stress.Item, h rehearseHooks, guestExtra []string) (r stress.Result, c rehearse.Challenge) {
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
	gargs = append(gargs, guestExtra...)
	reh, err := l.rehearseHooked(ctx, sr.session, it.Target.Server, identity, "default", it.Target.Instance, it.Mode, sargs, gargs, h)
	c = reh.challenge
	if c.ProofKey == "" && c.ProofPath == "" && reh.server != nil {
		// no challenge: the write failed, but it may have been applied
		// (a forwarded PATCH reset after the member applied it); the
		// record names the key it tried (INV-4)
		if k, ok := reh.server.observation("challenge-write.key"); ok {
			c.ProofKey, _ = k.(string)
		}
	}
	r.ServerEnd = serverEnd(reh.server)
	if err != nil {
		// LXD unreachable or too slow is the server half failing, not the
		// harness: it happens under faults that cut LXD off
		if reh.server != nil && (reh.server.code == exit.Connection || reh.server.code == exit.Timeout) {
			r.Outcome, r.FailedStep = stress.Fail, map[int]string{exit.Connection: "connect", exit.Timeout: "timeout"}[reh.server.code]
			r.Detail = err.Error()
			sr.leftovers(&r, it, c)
			return r, c
		}
		r.Outcome, r.Detail = stress.Error, err.Error()
		// a probe cut off after its challenge may have left the key or
		// the proof file: unchecked, the sweep found more than the
		// iterations had counted (INV-4)
		sr.leftovers(&r, it, c)
		return r, c
	}
	r.Steps = stepTimings(reh.server)
	if reh.guestErr != nil {
		r.Detail = "guest half: " + reh.guestErr.Error()
	}
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
		if err != nil || rm.rec.Outcome != "ok" {
			sr.leftovers(&r, it, c)
		}
	}
	if it.Mode == stress.FilePull && reh.challenge.ProofPath != "" && reh.guest == nil && reh.guestErr != nil {
		// a guest half cut off may have written the proof file first
		sr.leftovers(&r, it, c)
	}
	if it.Mode == stress.ConfigPush && r.Outcome != stress.Pass {
		// the cleanup may have failed or never run: the instance says
		sr.leftovers(&r, it, c)
		if v, ok := reh.server.observation("cleanup.result"); ok {
			r.Detail = strings.TrimSpace(r.Detail + " " + fmt.Sprint(v))
		}
	}
	return r, c
}

// serverEnd is when the server half's record says it ended (its start
// plus its duration, measured by the probe itself), so that INV-1 judges
// the recheck by the probe's clock, not by when the harness noticed;
// zero when there is no record.
func serverEnd(r *run) time.Time {
	if r == nil || r.rec == nil || r.rec.Time.IsZero() {
		return time.Time{}
	}
	return r.rec.Time.Add(time.Duration(r.rec.DurationMS) * time.Millisecond)
}

// checkLeftovers reports whether the iteration's challenge key
// (config_push) or proof file (file_pull) is still on the instance
// (INV-4), and whether the instance could tell: lxc exec fails the same
// way for a missing file and for an instance out of reach, so the guest
// says which.
func (sr *stressRun) checkLeftovers(it stress.Item, c rehearse.Challenge) (int, bool) {
	server, inst := it.Target.Server, it.Target.Instance
	switch {
	case c.ProofKey != "":
		out, err := sr.l.trySSH(server, "sudo timeout -k 5 20 lxc config get "+inst+" "+c.ProofKey)
		return parseLeftover(out, err, false)
	case c.ProofPath != "":
		out, err := sr.l.trySSH(server, "sudo timeout -k 5 20 lxc exec "+inst+" -- sh -c 'if test -e "+c.ProofPath+"; then echo present; else echo absent; fi'")
		return parseLeftover(out, err, true)
	}
	return 0, true
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

// orphanVolumes are the server's instance volumes whose instance is not
// on their member, as name@member.
func (l *lab) orphanVolumes(t *testing.T, server string) []string {
	t.Helper()
	vols := l.ssh(t, server, "sudo timeout -k 5 30 lxc storage volume list default -f csv")
	columns := "n"
	if l.env.Servers[server].Cluster == "yes" {
		columns = "nL" // L is refused when not clustered
	}
	insts := l.ssh(t, server, "sudo timeout -k 5 30 lxc list -c "+columns+" -f csv")
	return orphanVolumes(vols, insts)
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
	// a run that broke an invariant is not collected: it stays in the
	// working evidence for analysis
	if os.Getenv("LAB_COLLECT") == "yes" && !t.Failed() && sum.OK() {
		dst := filepath.Join(l.repo, "docs", "validation", name)
		if err := copyTree(dir, dst, nil); err != nil {
			t.Fatalf("collecting the summary: %v", err)
		}
		t.Logf("collected: %s", dst)
	}
}

// requireIterations fails a session that ran nothing: its invariants
// would hold vacuously.
func requireIterations(t *testing.T, results []stress.Result) {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("no iteration ran: check the LAB_STRESS_* settings")
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
	requireIterations(t, results)
	sr.recheckLeftovers(t, results)
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
		sr.recheckLeftovers(t, step)
		t.Logf("%s: %d iterations", sr.label, len(step))
		results = append(results, step...)
	}
	requireIterations(t, results)
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
	// the 60 s window only bounds the dispatch: an iteration that started
	// runs to its end, cleanup included, under its own timeout, so that the
	// end of the burst kills nothing halfway (a killed iteration left a
	// proof file in the first run)
	window, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	paced := func(_ context.Context, it stress.Item) stress.Result {
		select {
		case <-tick.C:
		case <-window.Done():
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return sr.iterate(ctx, it)
	}
	results := stress.Run(window, plan.Items(), 16, paced)
	requireIterations(t, results)
	settings := set.record()
	settings["session"], settings["offered"] = "ST-5", "20/s for 60 s, 16 in flight"
	sr.recheckLeftovers(t, results)
	sum := stress.Summarize(results, settings, stressBounds)
	l.writeSummary(t, stamp+"-st-5-summary", sum, results)
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

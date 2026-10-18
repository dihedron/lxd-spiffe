//go:build lab

package acceptance

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/test/lab/stress"
)

// The chaos sessions (stress spec, Fault catalogue): each fault has its
// injection hooks, a clean-up that ends its window, and a recovery that
// shows the instance passes again (INV-3).

// chaosFaults are ST-3's faults: the catalogue's but the cluster ones.
var chaosFaults = []string{"C1", "C2", "C3", "C4", "C5", "C6", "C9", "C10"}

// chaosRun is a stressRun with faults, the recoveries they caused, and a
// lock per server so that two server-wide faults never overlap.
type chaosRun struct {
	*stressRun
	mu         sync.Mutex
	recoveries []stress.Result
	serverMu   map[string]*sync.Mutex
}

func newChaosRun(sr *stressRun, servers []string) *chaosRun {
	c := &chaosRun{stressRun: sr, serverMu: map[string]*sync.Mutex{}}
	for _, s := range servers {
		c.serverMu[s] = &sync.Mutex{}
	}
	return c
}

// injection is one fault being performed.
type injection struct {
	hooks      rehearseHooks
	guestExtra []string
	start      time.Time // when the fault was injected; zero: the iteration's start
	// cleared is when the fault stopped acting, when it is known more
	// precisely than the end of its clean-up (H1)
	mu      sync.Mutex
	cleared time.Time
	// cleanup undoes the fault and returns when it is over; nil: nothing
	// to undo, the window ends with the iteration
	cleanup func() error
	detail  []string
}

func (inj *injection) clear(at time.Time) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	if inj.cleared.IsZero() {
		inj.cleared = at
	}
}

func (inj *injection) mark() {
	if inj.start.IsZero() {
		inj.start = time.Now()
	}
}

// ssh runs a command on a VM from a worker: errors become details.
func (inj *injection) ssh(l *lab, vm, command string) {
	if out, err := l.trySSH(vm, command); err != nil {
		inj.detail = append(inj.detail, fmt.Sprintf("%s: %v %s", command, err, strings.TrimSpace(out)))
	}
}

// waitFor polls check every second until it succeeds or the deadline.
func waitFor(deadline time.Duration, check func() bool) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if check() {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("not back within %s", deadline)
}

// prepare builds the injection of it's fault.
func (c *chaosRun) prepare(it stress.Item) *injection {
	l, server, inst := c.l, it.Target.Server, it.Target.Instance
	inj := &injection{}
	lxdUp := func() bool {
		_, err := l.trySSH(server, "sudo timeout 10 lxc info >/dev/null")
		return err == nil
	}
	switch it.Fault {
	case "C1": // instance stopped after the challenge
		inj.hooks.afterChallenge = func() {
			inj.mark()
			inj.ssh(l, server, "sudo lxc stop --force "+inst)
		}
		inj.cleanup = func() error { return c.ensureRunning(inj, server, inst) }
	case "C2": // instance restarted after the guest's answer
		inj.hooks.afterGuest = func() {
			inj.mark()
			inj.ssh(l, server, "sudo lxc restart --force "+inst)
		}
		inj.cleanup = func() error { return c.ensureRunning(inj, server, inst) }
	case "C3": // the LXD daemon killed: systemd starts it again, the
		// instances keep running (snap restart lxd would stop them all)
		locked := false
		var watching sync.WaitGroup
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			// the window starts once the kill has returned: a recheck just
			// before it may still have reached LXD
			inj.ssh(l, server, "sudo kill -9 $(pgrep -x lxd)")
			inj.mark()
			// a watcher notes when LXD answers again, while the iteration
			// goes on (H1)
			watching.Go(func() {
				if waitFor(3*time.Minute, lxdUp) == nil {
					inj.clear(time.Now())
				}
			})
		}
		inj.cleanup = func() error {
			if !locked {
				return nil // the iteration never reached the challenge
			}
			defer c.serverMu[server].Unlock()
			watching.Wait()
			return waitFor(3*time.Minute, lxdUp)
		}
	case "C4": // server half killed after the challenge
		inj.hooks.killServer = true
	case "C5": // guest half never answers
		inj.hooks.skipGuest = true
	case "C6": // partition of 8443 for 5 to 20 s, reverted by the VM itself
		seconds := 5 + it.N%16
		unit := fmt.Sprintf("lab-c6-%d-%d", it.N, time.Now().UnixNano()%100000)
		rule := "INPUT -p tcp --dport 8443 -j DROP"
		locked := false
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			// the revert is armed before the cut, so the lab recovers even if
			// the harness dies here (STR-61)
			inj.ssh(l, server, fmt.Sprintf("sudo systemd-run --quiet --unit=%s --on-active=%ds iptables -D %s && sudo iptables -I %s", unit, seconds, rule, rule))
			inj.mark()
			inj.clear(inj.start.Add(time.Duration(seconds) * time.Second)) // the VM's timer removes the rule then
		}
		inj.cleanup = func() error {
			if !locked {
				return nil // the iteration never reached the challenge
			}
			defer c.serverMu[server].Unlock()
			return waitFor(time.Duration(seconds+30)*time.Second, func() bool {
				_, err := l.trySSH(server, "sudo iptables -C "+rule+" 2>/dev/null")
				return err != nil
			})
		}
	case "C9": // a concurrent writer on the instance until the challenge
		stop := make(chan struct{})
		var wg sync.WaitGroup
		inj.mark()
		wg.Go(func() {
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = l.trySSH(server, fmt.Sprintf("sudo lxc config set %s user.lxd-probe.c9=%d", inst, n))
			}
		})
		once := sync.Once{}
		halt := func() { once.Do(func() { close(stop); wg.Wait(); inj.clear(time.Now()) }) }
		inj.hooks.afterChallenge = halt
		inj.cleanup = func() error {
			halt()
			inj.ssh(l, server, "sudo lxc config unset "+inst+" user.lxd-probe.c9")
			return nil
		}
	case "C10": // the guest writes or echoes a wrong proof
		inj.guestExtra = []string{"--tamper"}
	}
	return inj
}

// ensureRunning restores the instance after C1 or C2: an LXD crash during
// the stop, start or restart loses the operation and leaves the instance
// stopped (finding F2), so it is started whenever it is found stopped,
// for up to five minutes, until it answers. A repeated start is noted.
func (c *chaosRun) ensureRunning(inj *injection, server, inst string) error {
	starts := 0
	err := waitFor(5*time.Minute, func() bool {
		if _, err := c.l.trySSH(server, "sudo timeout 10 lxc exec "+inst+" -- true"); err == nil {
			return true
		}
		if out, err := c.l.trySSH(server, `sudo lxc list -c s -f csv "^`+inst+`$"`); err == nil && strings.TrimSpace(out) == "STOPPED" {
			starts++
			_, _ = c.l.trySSH(server, "sudo lxc start "+inst)
		}
		return false
	})
	if starts > 1 {
		inj.detail = append(inj.detail, fmt.Sprintf("started %d times: an operation was lost", starts))
	}
	return err
}

// iterate runs one item, with its fault if it has one: the iteration, the
// clean-up, the fault's window, the leftovers of a crash (C4), and the
// recovery iterations (INV-3).
func (c *chaosRun) iterate(ctx context.Context, it stress.Item) stress.Result {
	if it.Fault == "" {
		return c.stressRun.iterate(ctx, it)
	}
	inj := c.prepare(it)
	start := time.Now()
	r, ch := c.iterateHooked(ctx, it, inj.hooks, inj.guestExtra)
	if inj.start.IsZero() {
		inj.start = start
	}
	if inj.cleanup != nil {
		if err := inj.cleanup(); err != nil {
			inj.detail = append(inj.detail, "clean-up: "+err.Error())
		}
	}
	end := time.Now()
	if !inj.cleared.IsZero() {
		end = inj.cleared
	}
	r.Window = &stress.Window{Fault: it.Fault, Server: it.Target.Server, Instance: it.Target.Instance, Start: inj.start, End: end}
	if it.Fault == "C4" {
		r.Outcome, r.FailedStep = stress.Fail, "killed"
		r.Leftovers = c.checkLeftovers(it, ch)
	}
	if len(inj.detail) > 0 {
		r.Detail = strings.TrimSpace(r.Detail + " " + strings.Join(inj.detail, "; "))
	}
	c.recover(it)
	return r
}

// recover runs the item again without fault until it passes, every five
// seconds for up to five minutes, and keeps each try as a recovery result:
// crashes elsewhere on the server restart its containers, so an instance
// may only recover once they stop (INV-3 measures from then). Each try has
// its own timeout: a long clean-up must not starve it.
func (c *chaosRun) recover(it stress.Item) {
	clean := it
	clean.Fault = ""
	deadline := time.Now().Add(5 * time.Minute)
	for try := 1; time.Now().Before(deadline); try++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		r := c.stressRun.iterate(ctx, clean)
		cancel()
		r.Fault, r.Recovery, r.Attempts = it.Fault, true, try
		c.mu.Lock()
		c.recoveries = append(c.recoveries, r)
		c.mu.Unlock()
		if r.Outcome == stress.Pass {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// sweep counts and removes every challenge key and proof file left on the
// server's fleet and baseline (STR-30).
func (l *lab) sweep(t *testing.T, server string, instances []string) int {
	t.Helper()
	script := `n=0; for i in ` + strings.Join(instances, " ") + `; do ` +
		`for k in $(sudo lxc config show "$i" | sed -n 's/^  \(user\.spire\.challenge\.[0-9a-f]*\):.*/\1/p'); do sudo lxc config unset "$i" "$k"; n=$((n+1)); done; ` +
		`f=$(sudo timeout 20 lxc exec "$i" -- sh -c 'c=$(ls /run/spire/lxd 2>/dev/null | grep -c "^proof-"); rm -f /run/spire/lxd/proof-* /run/spire/lxd/.proof-*; echo $c' 2>/dev/null || echo 0); n=$((n+f)); done; echo $n`
	out := strings.TrimSpace(l.ssh(t, server, script))
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Errorf("%s: sweep: %q", server, out)
	}
	return n
}

// TestST3Chaos is ST-3 (STR-11): a seeded soak over the fleet and the
// baseline of the standalone servers, both modes, with faults C1–C6, C9
// and C10 injected halfway at LAB_STRESS_FAULT_RATE; every invariant is
// checked, and the session ends with the sweep (STR-30). LAB_STRESS_FAULTS
// (comma-separated) narrows the faults; LAB_STRESS_SEED replays a run.
func TestST3Chaos(t *testing.T) {
	l := newLab(t)
	set := loadStressSettings(t)
	faults := chaosFaults
	if v := os.Getenv("LAB_STRESS_FAULTS"); v != "" {
		faults = strings.Split(v, ",")
		for _, f := range faults {
			if ft, ok := stress.Lookup(f); !ok || ft.Cluster {
				t.Fatalf("LAB_STRESS_FAULTS: %q is not a standalone fault of the catalogue", f)
			}
		}
	}
	names := fleetNames(set.fleet)
	l.buildFleets(t, names)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	sr := &stressRun{l: l, session: session{evidenceDir: l.evidence, runID: stamp + "-st-3"}, label: "chaos"}
	cr := newChaosRun(sr, l.servers())
	targets := l.stressTargets(names)
	plan := stress.Plan{Seed: set.seed, Iterations: set.iterations * 2 * len(l.servers()), FaultRate: set.faultRate,
		Faults: faults, Modes: []string{rehearse.FilePull, rehearse.ConfigPush}, Targets: targets}
	start := time.Now()
	per := func(ctx context.Context, it stress.Item) stress.Result {
		ictx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return cr.iterate(ictx, it)
	}
	results := stress.Run(context.Background(), plan.Items(), set.workers, per)
	t.Logf("%d iterations and %d recoveries in %s", len(results), len(cr.recoveries), time.Since(start).Round(time.Second))
	requireIterations(t, results)
	settings := set.record()
	settings["session"], settings["faults"] = "ST-3", strings.Join(faults, ",")
	sum := stress.Summarize(append(results, cr.recoveries...), settings, stressBounds)
	swept := 0
	for _, server := range l.servers() {
		swept += l.sweep(t, server, append(append([]string{}, names...), stressBaseline...))
	}
	sum.Sweep(swept)
	l.writeSummary(t, stamp+"-st-3-summary", sum, append(results, cr.recoveries...))
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

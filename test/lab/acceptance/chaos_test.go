//go:build lab

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// fleet is each server's targets; busy, the targets a fault is acting
	// on, which a server-wide clean-up leaves alone
	fleet map[string][]string
	busy  map[stress.Target]int
	// failedCleanups are the clean-ups that could not undo their fault:
	// the faults after them run on a changed lab, so the session fails
	failedCleanups []string
	// session ends the plan's dispatch when abort cancels it, with why: a
	// clean-up that failed left a lab that makes the iterations after it
	// worthless
	session context.Context
	abort   context.CancelCauseFunc
}

// stopSession ends the session: no new iteration or recovery starts,
// those in flight finish.
func (c *chaosRun) stopSession(err error) {
	if c.abort != nil {
		c.abort(err)
	}
}

// stopped reports whether stopSession was called.
func (c *chaosRun) stopped() bool {
	return c.session != nil && c.session.Err() != nil
}

func newChaosRun(sr *stressRun, targets []stress.Target) *chaosRun {
	c := &chaosRun{stressRun: sr, serverMu: map[string]*sync.Mutex{}, fleet: map[string][]string{}, busy: map[stress.Target]int{}}
	c.session, c.abort = context.WithCancelCause(context.Background())
	for _, tg := range targets {
		if c.serverMu[tg.Server] == nil {
			c.serverMu[tg.Server] = &sync.Mutex{}
		}
		c.fleet[tg.Server] = append(c.fleet[tg.Server], tg.Instance)
	}
	return c
}

// injection is one fault being performed.
type injection struct {
	hooks      rehearseHooks
	guestExtra []string
	start      time.Time // when the fault was injected; zero: the iteration's start
	// notApplicable, when set, says why the fault was not injected: the
	// iteration runs without it
	notApplicable string
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

// virsh powers a lab VM off (destroy) or on (start) from the lab host.
func (inj *injection) virsh(action, vm string) {
	if out, err := exec.Command("virsh", "-c", "qemu:///system", action, "lxd-lab-"+vm).CombinedOutput(); err != nil {
		inj.detail = append(inj.detail, fmt.Sprintf("virsh %s %s: %v %s", action, vm, err, strings.TrimSpace(string(out))))
	}
}

// faultCommandTimeout bounds each command of a fault or its clean-up on
// the VM: a VM's move copies its disk, about two minutes in the lab.
const faultCommandTimeout = 5 * time.Minute

// ssh runs a command on a VM from a worker, bounded there by
// faultCommandTimeout: errors become details.
func (inj *injection) ssh(l *lab, vm, command string) {
	if out, err := l.trySSH(vm, boundedRemote(faultCommandTimeout, command)); err != nil {
		inj.detail = append(inj.detail, fmt.Sprintf("%s: %v %s", command, err, strings.TrimSpace(out)))
	}
}

// waitFor polls check every second until it succeeds or the deadline,
// and gives up at once when the session is stopping: the lab is broken
// then, and a clean-up waiting on it only delays the end (a stopped c6
// session went on for 20 minutes, five for each instance it waited on).
func (c *chaosRun) waitFor(deadline time.Duration, check func() bool) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if c.stopped() {
			return errors.New("given up: the session is stopping")
		}
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
	lxdUp := func(vm string) func() bool {
		return func() bool {
			_, err := l.trySSH(vm, "sudo timeout -k 5 10 lxc info >/dev/null")
			return err == nil
		}
	}
	switch it.Fault {
	case "C1": // instance stopped after the challenge
		inj.hooks.afterChallenge = func() {
			inj.mark()
			inj.ssh(l, server, "sudo lxc stop --force "+inst)
		}
		inj.cleanup = func() error { return c.ensureRunning(inj, server, inst, 5*time.Minute) }
	case "C2": // instance restarted after the guest's answer
		inj.hooks.afterGuest = func() {
			inj.mark()
			inj.ssh(l, server, "sudo lxc restart --force "+inst)
		}
		inj.cleanup = func() error { return c.ensureRunning(inj, server, inst, 5*time.Minute) }
	case "C3": // the LXD daemon killed: systemd starts it again, the
		// instances keep running (snap restart lxd would stop them all)
		locked := false
		var watching sync.WaitGroup
		// the killed daemon is the one watched: on a cluster the entry
		// member never went down, and watching it ended the window at
		// once, letting the next server-wide fault hit the member while
		// its daemon still restarted (ST-4)
		target := server
		up := func() bool {
			return lxdUp(target)() && (!c.isCluster(server) || c.healthy(server))
		}
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			target = c.faultVM(server, inst)
			// the window starts once the kill has returned: a recheck just
			// before it may still have reached LXD
			inj.ssh(l, target, "sudo kill -9 $(pgrep -x lxd)")
			inj.mark()
			// a watcher notes when LXD answers again, while the iteration
			// goes on (H1)
			watching.Go(func() {
				if c.waitFor(3*time.Minute, up) == nil {
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
			if err := c.waitFor(3*time.Minute, up); err != nil {
				return fmt.Errorf("LXD on %s: %w", target, err)
			}
			if err := c.ensureRunning(inj, server, inst, 5*time.Minute); err != nil {
				return err
			}
			return c.ensureFleetRunning(inj, server, inst)
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
		target := server
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			// the revert is armed before the cut, so the lab recovers even if
			// the harness dies here (STR-61)
			target = c.faultVM(server, inst)
			inj.ssh(l, target, fmt.Sprintf("sudo systemd-run --quiet --unit=%s --on-active=%ds iptables -D %s && sudo iptables -I %s", unit, seconds, rule, rule))
			inj.mark()
			inj.clear(inj.start.Add(time.Duration(seconds) * time.Second)) // the VM's timer removes the rule then
		}
		inj.cleanup = func() error {
			if !locked {
				return nil // the iteration never reached the challenge
			}
			defer c.serverMu[server].Unlock()
			return c.waitFor(time.Duration(seconds+30)*time.Second, func() bool {
				_, err := l.trySSH(target, "sudo iptables -C "+rule+" 2>/dev/null")
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
				_, _ = l.trySSH(server, boundedRemote(20*time.Second, fmt.Sprintf("sudo lxc config set %s user.lxd-probe.c9=%d", inst, n)))
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
	case "C7a", "C7b": // a cluster member powered off: forwarding, or holding
		holder := c.holder(server, inst)
		member := holder
		if it.Fault == "C7a" {
			member = c.otherMember(server, holder)
		}
		if member == c.l.vmOf(server) || member == "" {
			// the entry member down would cut the probe off: not this fault
			inj.notApplicable = "the instance is on the entry member"
			break
		}
		locked := false
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			inj.virsh("destroy", member)
			inj.mark()
		}
		inj.cleanup = func() error {
			if !locked {
				return nil
			}
			defer c.serverMu[server].Unlock()
			inj.virsh("start", member)
			if err := c.waitFor(5*time.Minute, func() bool { return c.healthy(server) }); err != nil {
				return fmt.Errorf("member %s: %w", member, err)
			}
			// a power-off while LXD writes its image stream cache leaves it
			// empty: LXD then fails every launch from a remote image on that
			// member, and once the files are gone its next launch hangs in
			// "create" until its daemon restarts (seen on 5.21.8). The remedy
			// an operator would apply is applied here, and noted.
			if out, _ := c.l.trySSH(member, boundedRemote(time.Minute, "sudo find /var/snap/lxd/common/lxd/cache -maxdepth 3 -type f -size 0 -print -delete")); strings.TrimSpace(out) != "" {
				inj.detail = append(inj.detail, "LXD's image cache was truncated by the power-off ("+strings.Join(strings.Fields(out), " ")+"): emptied files removed, LXD restarted on "+member)
				_, _ = c.l.trySSH(member, boundedRemote(2*time.Minute, "sudo systemctl restart snap.lxd.daemon"))
				if err := c.waitFor(5*time.Minute, func() bool { return c.healthy(server) }); err != nil {
					return fmt.Errorf("member %s after the LXD restart: %w", member, err)
				}
			}
			inj.clear(time.Now())
			if err := c.ensureRunning(inj, server, inst, 5*time.Minute); err != nil {
				return err
			}
			return c.ensureFleetRunning(inj, server, inst)
		}
	case "C8a": // the instance moved to another member, cold
		// under the server's lock: a move to or from a member being
		// evacuated or powered off fails, and the instance, not moved,
		// could pass the iteration this fault must fail (ST-4)
		locked := false
		var holder, other string
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			holder = c.holder(server, inst)
			other = c.otherMember(server, holder)
			inj.mark()
			inj.ssh(l, server, fmt.Sprintf("sudo lxc stop --force %s; sudo lxc move %s --target %s && sudo lxc start %s", inst, inst, other, inst))
		}
		inj.cleanup = func() error {
			if !locked {
				return nil // the iteration never reached the challenge
			}
			defer c.serverMu[server].Unlock()
			inj.ssh(l, server, fmt.Sprintf("sudo lxc stop --force %s; sudo lxc move %s --target %s; sudo lxc start %s", inst, inst, holder, inst))
			if now := c.holder(server, inst); now != holder {
				return fmt.Errorf("%s is on %q, not back on %s", inst, now, holder)
			}
			return c.ensureRunning(inj, server, inst, 5*time.Minute)
		}
	case "C8b": // the instance's member evacuated, then restored
		holder := c.holder(server, inst)
		if holder == c.l.vmOf(server) || holder == "" {
			// the probe's entry member stays up, as behind a load balancer
			// (STR-03); an evacuated entry member that could not be
			// restored left the cluster broken for the rest of a session
			inj.notApplicable = "the instance is on the entry member"
			break
		}
		flag := "--yes"
		if strings.HasPrefix(c.l.env.Servers[server].Version, "5.") {
			flag = "--force" // 5.21 skips the prompt with --force (gate L8)
		}
		locked := false
		inj.hooks.afterChallenge = func() {
			c.serverMu[server].Lock()
			locked = true
			inj.mark()
			inj.ssh(l, server, fmt.Sprintf("sudo lxc cluster evacuate %s %s", holder, flag))
		}
		inj.cleanup = func() error {
			if !locked {
				return nil
			}
			defer c.serverMu[server].Unlock()
			if err := c.restoreMember(inj, server, holder); err != nil {
				return err
			}
			inj.clear(time.Now())
			if err := c.ensureRunning(inj, server, inst, 5*time.Minute); err != nil {
				return err
			}
			return c.ensureFleetRunning(inj, server, inst)
		}
	}
	return inj
}

// isCluster reports whether server is a cluster of the lab.
func (c *chaosRun) isCluster(server string) bool {
	return c.l.env.Servers[server].Cluster == "yes"
}

// holder is the member that holds the instance; "" on a standalone server.
func (c *chaosRun) holder(server, inst string) string {
	if !c.isCluster(server) {
		return ""
	}
	out, err := c.l.trySSH(server, boundedRemote(30*time.Second, `sudo lxc list -c L -f csv "^`+inst+`$"`))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// otherMember is a member that is neither the entry member nor skip.
func (c *chaosRun) otherMember(server, skip string) string {
	for _, m := range c.l.members(server) {
		if m != c.l.vmOf(server) && m != skip {
			return m
		}
	}
	return ""
}

// faultVM is where a server-wide fault acts: the server's VM, or on a
// cluster a member that is not the entry, the holding one when it can be.
func (c *chaosRun) faultVM(server, inst string) string {
	if !c.isCluster(server) {
		return server
	}
	if h := c.holder(server, inst); h != "" && h != c.l.vmOf(server) {
		return h
	}
	return c.otherMember(server, "")
}

// healthy reports whether every member of the cluster is ONLINE and a
// voter of its database: on 6.9 an evacuated member loses its database
// role, and a fault on one of the two voters left then took the cluster
// below quorum (ST-4 on c6).
func (c *chaosRun) healthy(server string) bool {
	out, err := c.l.trySSH(server, "sudo timeout -k 5 10 lxc cluster list -f csv")
	return err == nil && clusterHealthy(out, len(c.l.members(server)))
}

// reportStop fails the session when a failed clean-up stopped it.
func (c *chaosRun) reportStop(t *testing.T, ran, planned int) {
	t.Helper()
	if c.stopped() {
		t.Errorf("the session stopped after %d of %d iterations: %v", ran, planned, context.Cause(c.session))
	}
}

// reportCleanups fails the session for each clean-up that could not undo
// its fault (stress spec, Fault catalogue): it ran on, but on a lab the
// faults left changed.
func (c *chaosRun) reportCleanups(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.failedCleanups {
		t.Errorf("clean-up failed: %s", f)
	}
}

// leftoverVolume is LXD's error when a restore meets an instance volume
// the evacuation could not delete on the member (seen on 5.21.8).
const leftoverVolume = "already exists on migration target storage"

// restoreMember restores an evacuated member, trying again for up to five
// minutes; each failure is noted, and the member must end up ONLINE. A
// restore that fails three times on a leftover volume never succeeds: the
// member stays evacuated until the volume is removed by hand, so it gives
// up there, the finding in its error. Restore skips its confirmation with
// --force on 5.21 and 6.x alike (gate L11): 6.x's --yes is evacuate's.
func (c *chaosRun) restoreMember(inj *injection, server, member string) error {
	restore := boundedRemote(3*time.Minute, "sudo lxc cluster restore "+member+" --force")
	leftovers, last := 0, ""
	err := c.waitFor(5*time.Minute, func() bool {
		out, err := c.l.trySSH(server, restore)
		if err != nil && !strings.Contains(out, "already restored") {
			last = lastLine(out)
			inj.detail = append(inj.detail, fmt.Sprintf("%s: %v %s", restore, err, last))
			if strings.Contains(out, leftoverVolume) {
				if leftovers++; leftovers == 3 {
					return true
				}
			}
			time.Sleep(10 * time.Second)
		}
		return c.healthy(server)
	})
	if leftovers == 3 {
		return fmt.Errorf("member %s cannot be restored, LXD left an instance volume on it (a finding; lab.sh reset): %s", member, last)
	}
	if err != nil {
		return fmt.Errorf("member %s not restored: %w", member, err)
	}
	return nil
}

// lastLine is the last non-empty line of out: lxc's error after its
// progress lines.
func lastLine(out string) string {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(out), "\r", "\n"), "\n")
	return strings.TrimSpace(parts[len(parts)-1])
}

// ensureRunning restores the instance after C1 or C2: an LXD crash during
// the stop, start or restart loses the operation and leaves the instance
// stopped (finding F2), so it is started whenever it is found stopped,
// for up to within, until it answers. A repeated start is noted.
func (c *chaosRun) ensureRunning(inj *injection, server, inst string, within time.Duration) error {
	starts := 0
	err := c.waitFor(within, func() bool {
		if _, err := c.l.trySSH(server, "sudo timeout -k 5 10 lxc exec "+inst+" -- true"); err == nil {
			return true
		}
		if out, err := c.l.trySSH(server, boundedRemote(30*time.Second, `sudo lxc list -c s -f csv "^`+inst+`$"`)); err == nil && strings.TrimSpace(out) == "STOPPED" {
			starts++
			_, _ = c.l.trySSH(server, boundedRemote(2*time.Minute, "sudo lxc start "+inst))
		}
		return false
	})
	if starts > 1 {
		inj.detail = append(inj.detail, fmt.Sprintf("started %d times: an operation was lost", starts))
	}
	return err
}

// ensureFleetRunning starts again the server's fleet instances that a
// server-wide fault left stopped (C3, C7, C8b), but for own, which the
// fault's clean-up handles, and those another fault is acting on: a
// daemon crash or a power-off can leave other instances on the member
// stopped (finding F2), and the next iterations would only meet the
// policy's not_running, not the attestor's recovery (INV-3). Each start
// is noted: it is what an operator would have to do.
func (c *chaosRun) ensureFleetRunning(inj *injection, server, own string) error {
	out, err := c.l.trySSH(server, "sudo timeout -k 5 20 lxc list -c ns -f csv")
	if err != nil {
		return fmt.Errorf("listing the instances: %w", err)
	}
	c.mu.Lock()
	stopped := stoppedInstances(out, c.fleet[server], func(name string) bool {
		return name == own || c.busy[stress.Target{Server: server, Instance: name}] > 0
	})
	c.mu.Unlock()
	// one deadline for all: each instance's own five minutes added up
	// to a quarter of an hour on a broken cluster
	deadline := time.Now().Add(5 * time.Minute)
	var errs []error
	for _, name := range stopped {
		inj.detail = append(inj.detail, name+" left stopped: started again")
		if err := c.ensureRunning(inj, server, name, time.Until(deadline)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// iterate runs one item, with its fault if it has one: the iteration, the
// clean-up, the fault's window, the leftovers of a crash (C4), and the
// recovery iterations (INV-3).
func (c *chaosRun) iterate(ctx context.Context, it stress.Item) stress.Result {
	if it.Fault == "" {
		return c.stressRun.iterate(ctx, it)
	}
	inj := c.prepare(it)
	if inj.notApplicable != "" {
		clean := it
		clean.Fault = ""
		r := c.stressRun.iterate(ctx, clean)
		r.Detail = strings.TrimSpace(r.Detail + " " + it.Fault + " not applicable: " + inj.notApplicable)
		return r
	}
	c.mu.Lock()
	c.busy[it.Target]++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.busy[it.Target]--
		c.mu.Unlock()
	}()
	start := time.Now()
	r, ch := c.iterateHooked(ctx, it, inj.hooks, inj.guestExtra)
	if inj.start.IsZero() {
		inj.start = start
	}
	if inj.cleanup != nil {
		if err := inj.cleanup(); err != nil {
			inj.detail = append(inj.detail, "clean-up: "+err.Error())
			failed := fmt.Sprintf("iteration %d, %s/%s, %s: %v", it.N, it.Target.Server, it.Target.Instance, it.Fault, err)
			c.mu.Lock()
			c.failedCleanups = append(c.failedCleanups, failed)
			c.mu.Unlock()
			// the lab is not the one the next faults expect: on c6, a member
			// left evacuated took the cluster below quorum at the next fault
			c.stopSession(errors.New("clean-up failed: " + failed))
		}
	}
	end := time.Now()
	if !inj.cleared.IsZero() {
		end = inj.cleared
	}
	r.Window = &stress.Window{Fault: it.Fault, Server: it.Target.Server, Instance: it.Target.Instance, Start: inj.start, End: end}
	if it.Fault == "C4" {
		r.Outcome, r.FailedStep = stress.Fail, "killed"
		c.leftovers(&r, it, ch)
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
	for try := 1; time.Now().Before(deadline) && !c.stopped(); try++ {
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
		`for k in $(sudo timeout -k 5 20 lxc config show "$i" | sed -n 's/^  \(user\.spire\.challenge\.[0-9a-f]*\):.*/\1/p'); do sudo timeout -k 5 20 lxc config unset "$i" "$k"; echo "left $i $k"; n=$((n+1)); done; ` +
		`f=$(sudo timeout -k 5 20 lxc exec "$i" -- sh -c 'for p in /run/spire/lxd/proof-*; do [ -e "$p" ] && echo "$p"; done; rm -f /run/spire/lxd/proof-* /run/spire/lxd/.proof-*' 2>/dev/null); ` +
		`for p in $f; do echo "left $i $p"; n=$((n+1)); done; done; echo $n`
	out := l.ssh(t, server, script)
	n, left, err := parseSweep(out)
	if err != nil {
		t.Errorf("%s: sweep: %v", server, err)
	}
	if len(left) > 0 {
		t.Logf("%s: the sweep removed %s", server, strings.Join(left, ", "))
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
	targets := l.stressTargets(names)
	cr := newChaosRun(sr, targets)
	plan := stress.Plan{Seed: set.seed, Iterations: set.iterations * 2 * len(l.servers()), FaultRate: set.faultRate,
		Faults: faults, Modes: []string{rehearse.FilePull, rehearse.ConfigPush}, Targets: targets}
	start := time.Now()
	per := func(ctx context.Context, it stress.Item) stress.Result {
		ictx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return cr.iterate(ictx, it)
	}
	results := stress.Run(cr.session, plan.Items(), set.workers, per)
	t.Logf("%d iterations and %d recoveries in %s", len(results), len(cr.recoveries), time.Since(start).Round(time.Second))
	requireIterations(t, results)
	cr.reportStop(t, len(results), len(plan.Items()))
	cr.reportCleanups(t)
	settings := set.record()
	settings["session"], settings["faults"] = "ST-3", strings.Join(faults, ",")
	all := append(results, cr.recoveries...)
	sr.recheckLeftovers(t, all)
	sum := stress.Summarize(all, settings, stressBounds)
	swept := 0
	for _, server := range l.servers() {
		swept += l.sweep(t, server, append(append([]string{}, names...), stressBaseline...))
	}
	sum.Sweep(swept)
	l.writeSummary(t, stamp+"-st-3-summary", sum, all)
	if !sum.OK() {
		t.Errorf("invariants: %+v", sum.Violations)
	}
}

// clusterFaults are ST-4's faults (stress spec, ST-4). C8b is not one:
// an evacuation can leave a volume that no restore gets past, on 5.21.8
// and 6.9 alike, and ST-6 measures it; C8a covers a move for the attestor.
var clusterFaults = []string{"C3", "C6", "C7a", "C7b", "C8a", "C9", "C10"}

// TestST4ClusterChaos is ST-4: on each running cluster in turn, a seeded
// soak over a fleet spread over the members, plus the cluster's baseline,
// with the cluster faults; every invariant; the sweep. The probe enters
// through the first member. LAB_STRESS_CLUSTER_FLEET (default 9),
// LAB_STRESS_ITERATIONS (default 200 here) and LAB_STRESS_FAULT_RATE
// (default 0.25 here) size it.
func TestST4ClusterChaos(t *testing.T) {
	l := newLab(t)
	clusters := l.clusters()
	if len(clusters) == 0 {
		t.Skip("ST-4 needs a running cluster: LAB_CLUSTER=5.21, 6.9 or both")
	}
	t.Setenv("LAB_STRESS_ITERATIONS", setting("LAB_STRESS_ITERATIONS", "200"))
	t.Setenv("LAB_STRESS_FAULT_RATE", setting("LAB_STRESS_FAULT_RATE", "0.25"))
	t.Setenv("LAB_STRESS_FLEET", setting("LAB_STRESS_CLUSTER_FLEET", "9"))
	set := loadStressSettings(t)
	for _, cluster := range clusters {
		t.Run(cluster, func(t *testing.T) {
			names := fleetNames(set.fleet)
			l.fleetSetup(t, cluster, names, l.members(cluster)...)
			stamp := time.Now().UTC().Format("20060102T150405Z")
			sr := &stressRun{l: l, session: session{evidenceDir: l.evidence, runID: stamp + "-st-4-" + cluster}, label: "cluster chaos"}
			instances := append(append([]string{}, names...), "c-unpriv", "v-ubuntu")
			var targets []stress.Target
			for _, n := range instances {
				targets = append(targets, stress.Target{Server: cluster, Instance: n})
			}
			cr := newChaosRun(sr, targets)
			plan := stress.Plan{Seed: set.seed, Iterations: set.iterations * 2, FaultRate: set.faultRate,
				Faults: clusterFaults, Modes: []string{rehearse.FilePull, rehearse.ConfigPush}, Targets: targets}
			start := time.Now()
			per := func(ctx context.Context, it stress.Item) stress.Result {
				ictx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				return cr.iterate(ictx, it)
			}
			results := stress.Run(cr.session, plan.Items(), set.workers, per)
			t.Logf("%d iterations and %d recoveries in %s", len(results), len(cr.recoveries), time.Since(start).Round(time.Second))
			requireIterations(t, results)
			cr.reportStop(t, len(results), len(plan.Items()))
			cr.reportCleanups(t)
			settings := set.record()
			settings["session"], settings["cluster"], settings["faults"] = "ST-4", cluster, strings.Join(clusterFaults, ",")
			all := append(results, cr.recoveries...)
			sr.recheckLeftovers(t, all)
			sum := stress.Summarize(all, settings, stressBounds)
			sum.Sweep(l.sweep(t, cluster, instances))
			l.writeSummary(t, stamp+"-st-4-"+cluster+"-summary", sum, all)
			if orphans := l.orphanVolumes(t, cluster); len(orphans) > 0 {
				t.Logf("LXD left instance volumes behind (an evacuation that failed deleting its source, a finding): %s; the next session needs lab.sh reset", strings.Join(orphans, " "))
			}
			if !sum.OK() {
				t.Errorf("invariants: %+v", sum.Violations)
			}
		})
	}
}

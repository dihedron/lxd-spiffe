//go:build lab

package acceptance

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
)

// The cases of the external review's fixes (attestor spec, Nov 7, 2026):
// SEC-09, FR-S3, FR-S15, FR-S18.

// reviewServers returns a healthy server and the one the test takes down.
func reviewServers(t *testing.T, l *lab) (healthy, down string) {
	t.Helper()
	servers := l.servers()
	if len(servers) < 2 {
		t.Skip("needs two standalone servers")
	}
	return servers[0], servers[1]
}

// fault applies inject on a VM and undoes it with revert when the test ends;
// the VM itself reverts it after 15 minutes, armed first, so that the lab
// recovers even if the harness dies (STR-61).
func (l *lab) fault(t *testing.T, vm, name, inject, revert string) (undo func()) {
	t.Helper()
	unit := fmt.Sprintf("e2e-%s-%d", name, time.Now().UnixNano()%100000)
	l.ssh(t, vm, fmt.Sprintf("sudo systemd-run --quiet --unit=%s --on-active=900 sh -c '%s' && sudo sh -c '%s'", unit, revert, inject))
	done := false
	undo = func() {
		if done {
			return
		}
		done = true
		if _, err := l.trySSH(vm, fmt.Sprintf("sudo systemctl stop %s.timer; sudo sh -c '%s'", unit, revert)); err != nil {
			t.Errorf("reverting %s on %s: %v", name, vm, err)
		}
	}
	t.Cleanup(undo)
	return undo
}

const (
	dropLXD  = "iptables -I INPUT -p tcp --dport 8443 -j DROP"
	allowLXD = "iptables -D INPUT -p tcp --dport 8443 -j DROP"
	// the LXD daemon, not its forkexec helpers or a guest's lxd-agent
	// (the bracket keeps pkill from matching the shell that runs it)
	stopLXD = "pkill -STOP -f \"/[l]xd --logfile\""
	contLXD = "pkill -CONT -f \"/[l]xd --logfile\""
)

func (l *lab) spireHealthy() bool {
	_, err := l.trySSH("spire", "sudo "+spirePrefix+"/bin/spire-server healthcheck")
	return err == nil
}

func (l *lab) logged(t *testing.T, mark int, text string) bool {
	t.Helper()
	return strings.Contains(l.logSince(t, mark), text)
}

// TestE2E6aServerDown: with one server cut off, an instance on another
// attests with a warning, one on the cut off server is refused with
// lxd_unavailable, the breaker opens, and it recovers without a restart.
func TestE2E6aServerDown(t *testing.T) {
	l := requirePlugins(t)
	healthy, down := reviewServers(t, l)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-6a-summary") })
	undo := l.fault(t, down, "6a", dropLXD, allowLXD)
	mark := l.logMark(t)
	cut := l.guestUp(t, down, "e2e-6a-down")
	g := l.guestUp(t, healthy, "e2e-6a-up")
	l.attested(t, g, "file_pull", run, "E2E-6a instance on a healthy server")
	if !l.logged(t, mark, "skipping an LXD server that cannot be reached") {
		t.Error("no warning about the skipped server")
	}
	waitFor(t, 5*time.Minute, "the refusal of the instance on the cut off server", func() bool {
		return slices.Contains(l.refusals(t, mark), "lxd_unavailable")
	})
	waitFor(t, 5*time.Minute, "the breaker to open", func() bool {
		return l.logged(t, mark, "circuit breaker is open")
	})
	undo()
	l.attested(t, cut, "file_pull", run, "E2E-6a instance after the server came back")
}

// TestE2E6bServerHung: with the LXD daemon of one server stopped, so that it
// accepts connections and never answers, an instance on another attests.
func TestE2E6bServerHung(t *testing.T) {
	l := requirePlugins(t)
	healthy, down := reviewServers(t, l)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-6b-summary") })
	l.fault(t, down, "6b", stopLXD, contLXD)
	mark := l.logMark(t)
	g := l.guestUp(t, healthy, "e2e-6b-up")
	l.attested(t, g, "file_pull", run, "E2E-6b instance on a healthy server")
	if !l.logged(t, mark, "skipping an LXD server that cannot be reached") {
		t.Error("no warning about the skipped server")
	}
}

// TestE2E6cTwinOnOtherServer: an import of an instance on another server,
// which answers, still refuses the instance (T7).
func TestE2E6cTwinOnOtherServer(t *testing.T) {
	l := requirePlugins(t)
	server, other := reviewServers(t, l)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-6c-summary") })
	g := l.guestUp(t, server, "e2e-6c")
	l.attested(t, g, "file_pull", run, "E2E-6c before the twin")
	const backup = "/tmp/e2e-6c.tar.gz"
	t.Cleanup(func() {
		for _, vm := range []string{server, other} {
			_, _ = l.trySSH(vm, "sudo rm -f "+backup)
		}
		_, _ = l.trySSH(other, "sudo lxc delete --force e2e-6c")
	})
	l.ssh(t, server, fmt.Sprintf("sudo lxc stop e2e-6c && sudo lxc export e2e-6c %s --instance-only && sudo lxc start e2e-6c", backup))
	if err := l.transfer(server, other, backup); err != nil {
		t.Fatal(err)
	}
	l.ssh(t, other, "sudo lxc import "+backup)
	mark := l.logMark(t)
	l.ssh(t, server, "sudo lxc exec e2e-6c -- sh -c 'until systemctl is-system-running --quiet --wait; do sleep 1; done; systemctl stop spire-agent; rm -rf /var/lib/spire/agent/*; systemctl start spire-agent'")
	waitFor(t, 5*time.Minute, "the refusal of the instance with a twin", func() bool {
		return slices.Contains(l.refusals(t, mark), "instance_ambiguous")
	})
	run.add(e2eResult{Test: "E2E-6c with a twin on the other server", Server: server, Guest: g.name, Kind: g.kind, Reasons: l.refusals(t, mark)})
}

// TestE2E7NoLockoutByClaim: failed attestations naming an instance do not
// keep the instance from attesting (SEC-09).
func TestE2E7NoLockoutByClaim(t *testing.T) {
	l := requirePlugins(t)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-7-summary") })
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			mark := l.logMark(t)
			l.guestUp(t, server, "e2e-7-attacker", "--claim-name", "e2e-7-victim")
			count := func(reason string) int {
				n := 0
				for _, r := range l.refusals(t, mark) {
					if r == reason {
						n++
					}
				}
				return n
			}
			waitFor(t, 5*time.Minute, "two failed attestations naming the victim", func() bool {
				return count("instance_not_found") >= 2
			})
			g := l.guestUp(t, server, "e2e-7-victim")
			l.attested(t, g, "file_pull", run, "E2E-7 the instance others failed to attest as")
			if n := count("rate_limited"); n != 0 {
				t.Errorf("%d attestations refused with rate_limited", n)
			}
			run.add(e2eResult{Test: "E2E-7 failed attempts before it", Server: server, Guest: "e2e-7-attacker", Kind: "container", Reasons: l.refusals(t, mark)})
		})
	}
}

// TestUpgradeRefusesFailureCooldown: a configuration that still sets
// limits.failure_cooldown is refused with a message naming it, and SPIRE
// Server starts again once it is removed.
func TestUpgradeRefusesFailureCooldown(t *testing.T) {
	l := requirePlugins(t)
	conf := filepath.Join("/etc/spire", "server.conf")
	l.ssh(t, "spire", fmt.Sprintf("sudo cp %[1]s %[1]s.review", conf))
	t.Cleanup(func() {
		l.ssh(t, "spire", fmt.Sprintf("sudo mv %[1]s.review %[1]s && sudo systemctl restart spire-server", conf))
		waitFor(t, 2*time.Minute, "SPIRE Server to be healthy again", func() bool {
			return l.spireHealthy()
		})
	})
	mark := l.logMark(t)
	l.ssh(t, "spire", fmt.Sprintf(`sudo sed -i '/proof_mode = /a limits { failure_cooldown = "5s" }' %s; sudo systemctl restart spire-server; true`, conf))
	waitFor(t, time.Minute, "the refusal of the configuration", func() bool {
		return l.logged(t, mark, "failure_cooldown")
	})
	if l.spireHealthy() {
		t.Error("SPIRE Server is healthy with limits.failure_cooldown set")
	}
}

// TestR1ProofDirMode: a rehearsal fails at step dir where the attestor would
// refuse the proof directory (PRS-50).
func TestR1ProofDirMode(t *testing.T) {
	l := newLab(t)
	const inst = "c-unpriv"
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			s := session{evidenceDir: l.evidence, runID: stamp + "-r-1-dir-" + server}
			dir := fmt.Sprintf("sudo lxc exec %s -- sh -c 'mkdir -p /run/spire/lxd && chmod %%s /run/spire/lxd'", inst)
			l.ssh(t, server, fmt.Sprintf(dir, "0755"))
			t.Cleanup(func() { l.ssh(t, server, fmt.Sprintf(dir, "0700")) })
			cell := []string{"--cell", server + "-" + inst + "-dir", "--gate", "R"}
			sargs := append([]string{"--strict", "--allow-privileged", "--expect-fail", "dir"}, cell...)
			gargs := append(append([]string{}, spireWrite...), cell...)
			reh := l.rehearse(t, s, server, "files", "default", inst, rehearse.FilePull, sargs, gargs)
			if want := "failed at dir, as expected"; reh.server.code != 0 || reh.server.result() != want {
				t.Errorf("%q, want %q: %s", reh.server.result(), want, reh.server)
			}
			rm := append([]string{"guest", "file", "rm", reh.challenge.ProofPath}, append(spireWrite, cell...)...)
			l.guest(t, s, server, "default", inst, rm...)
		})
	}
}

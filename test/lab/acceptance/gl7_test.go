//go:build lab

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleL7 is gate L7's decision rule (probe spec, Gate runbooks).
const ruleL7 = "L7 decision: the agent-start-order documentation for operators; which image families are supported; " +
	"Windows guests stay untested and unsupported."

// TestGL7Agent is G-L7, gate L7's session, one run per server: for each
// baseline VM, server file stat /etc/hostname once while it is stopped
// (Q2), then a start and two forced restarts (cold boots), each followed by
// server file stat --wait 5m as files, which times the files API from the
// start, and guest preflight.
// The VMs are running again at the end.
func TestGL7Agent(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	const path = "/etc/hostname"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l7-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			for _, vm := range []string{"v-ubuntu", "v-debian", "v-alpine"} {
				tags := []string{"--gate", "L7", "--cell", server + "-" + vm}
				// a VM still booting ignores the shutdown request, and a clean
				// stop or restart then waits without limit
				l.ssh(t, server, "sudo lxc stop --timeout 120 "+vm+" || sudo lxc stop --force "+vm)
				stopped := l.probe(t, s, server, "files", append([]string{"server", "file", "stat", vm, path}, tags...)...)
				status, _ := stopped.observation("file.http_status")
				msg, _ := stopped.observation("file.error")
				t.Logf("  %-9s stopped: %v %v", vm, status, msg)
				l.ssh(t, server, "sudo lxc start "+vm)
				for i := 1; i <= 3; i++ {
					if i > 1 {
						l.ssh(t, server, "sudo lxc restart --force "+vm) // a cold boot each time
					}
					r := l.probe(t, s, server, "files", append([]string{"server", "file", "stat", vm, path, "--wait", "5m"}, tags...)...)
					ready, _ := r.observation("wait.ready_after_ms")
					attempts, _ := r.observation("wait.attempts")
					t.Logf("  %-9s start %d: files API after %v ms, %v attempts, failures %s", vm, i, ready, attempts, failureView(r))
				}
				if err := l.waitAgent(server, vm); err != nil {
					t.Fatal(err)
				}
				l.guest(t, s, server, "default", vm, append([]string{"guest", "preflight"}, tags...)...)
				l.pull(t, server, "default", vm, runID)
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL7)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// failureView renders the distinct failures of file stat --wait.
func failureView(r *run) string {
	v, _ := r.observation("wait.failures")
	list, _ := v.([]any)
	out := ""
	for _, f := range list {
		m, _ := f.(map[string]any)
		out += fmt.Sprintf("[%v %q ×%v from %v ms] ", m["status"], m["error"], m["count"], m["first_seen_ms"])
	}
	if out == "" {
		return "none"
	}
	return out
}

// agentServiceScript reports how lxd-agent is started in a guest: the
// systemd unit (its id, state and what wants it) or the OpenRC service, so
// that the operator documentation can name what the SPIRE agent is ordered
// after.
const agentServiceScript = `if command -v systemctl >/dev/null 2>&1; then
  echo "systemd: $(systemctl show -p Id -p ActiveState -p UnitFileState -p WantedBy --value lxd-agent.service | paste -sd' ')"
elif command -v rc-service >/dev/null 2>&1; then
  echo "openrc: $(rc-service lxd-agent status 2>&1 | tr -s ' ' | paste -sd' '); runlevels: $(rc-update show 2>/dev/null | grep lxd-agent | tr -s ' ' | paste -sd' ')"
else
  echo "no systemd or openrc"
fi`

// selinuxScript reports the SELinux mode and the AVC denials of the
// current boot: in total, and those naming lxd-agent.
const selinuxScript = `mode=$(getenforce 2>/dev/null || echo absent)
since=$(date -d "$(uptime -s)" +%s)
avc=$(awk -v s="$since" -F'[(:.]' '/type=AVC/ && /denied/ { if ($2 >= s) n++ } END { print n+0 }' /var/log/audit/audit.log 2>/dev/null || echo unknown)
agent=$(awk -v s="$since" -F'[(:.]' '/type=AVC/ && /denied/ && /lxd-agent/ { if ($2 >= s) n++ } END { print n+0 }' /var/log/audit/audit.log 2>/dev/null || echo unknown)
echo "selinux $mode; AVC denials this boot: $avc, naming lxd-agent: $agent"`

// TestGL7Families completes G-L7, one run per server: the lxd-agent
// service of the baseline VMs (what the SPIRE agent is ordered after), and
// two RHEL 9 rebuilds with SELinux enforcing, l7-rocky and l7-alma,
// launched for the session from images:rockylinux/9/cloud and
// images:almalinux/9/cloud and deleted at the end: server file stat once
// while stopped, three cold boots each timed by file stat --wait 5m, the
// lxd-agent service, and the SELinux mode with the AVC denials of the
// boot. Calls go as full (the identities' grants name the baseline
// instances only). It fails only when the probe fails or the evidence is
// not sanitized, and with LAB_COLLECT=yes copies each run to
// docs/validation/.
func TestGL7Families(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	const path = "/etc/hostname"
	rhel := []struct{ name, image string }{
		{"l7-rocky", "images:rockylinux/9/cloud"},
		{"l7-alma", "images:almalinux/9/cloud"},
	}
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l7-families-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			teardown := "for i in l7-rocky l7-alma; do sudo lxc delete -f $i 2>/dev/null; done; true"
			l.ssh(t, server, teardown)
			t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort
			note := func(cell, text string) {
				l.util(t, s, "note", text, "--gate", "L7", "--cell", server+"-"+cell)
			}
			inVM := func(vm, script string) string {
				out, err := l.trySSH(server, "sudo timeout -k 5 60 lxc exec "+vm+" -- sh -c "+shellQuote(script))
				if err != nil {
					return "failed: " + inline(out+" "+err.Error())
				}
				return inline(out)
			}

			for _, vm := range []string{"v-ubuntu", "v-debian", "v-alpine"} {
				svc := inVM(vm, agentServiceScript)
				note(vm, "lxd-agent service: "+svc)
				t.Logf("  %-9s %s", vm, svc)
			}

			for _, r := range rhel {
				cell := "v-" + strings.TrimPrefix(r.name, "l7-")
				tags := []string{"--gate", "L7", "--cell", server + "-" + cell}
				if _, err := l.trySSH(server, "sudo timeout 20m lxc launch "+r.image+" "+r.name+" --vm"); err != nil {
					note(cell, "could not launch "+r.image+": "+inline(err.Error()))
					t.Errorf("launch %s: %v", r.image, err)
					continue
				}
				if err := l.waitAgent(server, r.name); err != nil {
					note(cell, r.image+": "+err.Error())
					t.Errorf("%s: %v", r.name, err)
					continue
				}
				release := inVM(r.name, ". /etc/os-release; echo $PRETTY_NAME")
				note(cell, "session-only VM "+r.name+" from "+r.image+" ("+release+")")
				l.ssh(t, server, "sudo lxc stop --timeout 120 "+r.name+" || sudo lxc stop --force "+r.name)
				stopped := l.probe(t, s, server, "full", append([]string{"server", "file", "stat", r.name, path}, tags...)...)
				status, _ := stopped.observation("file.http_status")
				msg, _ := stopped.observation("file.error")
				t.Logf("  %-9s %s; stopped: %v %v", cell, release, status, msg)
				l.ssh(t, server, "sudo lxc start "+r.name)
				for i := 1; i <= 3; i++ {
					if i > 1 {
						l.ssh(t, server, "sudo lxc restart --force "+r.name) // a cold boot each time
					}
					w := l.probe(t, s, server, "full", append([]string{"server", "file", "stat", r.name, path, "--wait", "5m"}, tags...)...)
					ready, _ := w.observation("wait.ready_after_ms")
					attempts, _ := w.observation("wait.attempts")
					t.Logf("  %-9s start %d: files API after %v ms, %v attempts, failures %s", cell, i, ready, attempts, failureView(w))
				}
				if err := l.waitAgent(server, r.name); err != nil {
					t.Fatal(err)
				}
				svc := inVM(r.name, agentServiceScript)
				sel := inVM(r.name, selinuxScript)
				note(cell, "lxd-agent service: "+svc)
				note(cell, sel)
				t.Logf("  %-9s %s; %s", cell, svc, sel)
			}

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL7)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// selinuxDropIn makes systemd run lxd-agent as bin_t, hence in
// unconfined_service_t, which may create its vsock socket (in init_t, the
// domain a var_run_t binary keeps, it may not). The copy of the agent in
// /run/lxd_agent is renewed on every boot, so it is relabelled on every
// start. For the session's control, a one-shot flag skips the relabel once
// under enforcing (set once the system runs enforcing: the relabel boot
// may consume it): that start fails, and systemd's restart five seconds
// later succeeds.
const selinuxDropIn = `[Service]
ExecStartPre=/bin/sh -c 'if [ -e /etc/lxd-probe-l7-skip-once ] && [ "$(getenforce)" = Enforcing ]; then rm -f /etc/lxd-probe-l7-skip-once; else /usr/bin/chcon -t bin_t /run/lxd_agent/lxd-agent; fi'
`

// selinuxStateScript reports the SELinux mode, the domain lxd-agent runs
// in, its binary's label, and the AVC denials of the boot.
const selinuxStateScript = `echo "selinux $(getenforce); agent $(ps -eo label,comm | awk '$2 == "lxd-agent" { print $1 }' | paste -sd' '); binary $(ls -Z /run/lxd_agent/lxd-agent | cut -d' ' -f1); AVC denials this boot: $(ausearch -m avc -ts boot 2>/dev/null | grep -c denied)"`

// TestGL7SELinux completes G-L7 for SELinux enforcing, one run per
// server: a session-only Rocky Linux 9 VM, l7-selinux, from
// images:rockylinux/9/cloud (which ships no SELinux policy), gets the
// targeted policy and audit, the lxd-agent drop-in (selinuxDropIn), a
// relabel, and boots enforcing. Then the control: a cold boot whose first
// agent start skips the drop-in once (the agent cannot create its vsock
// socket in init_t), recording that boot's journal and AVC denials, and
// the agent's domain and the AVC denials once systemd's restart runs it. Then three cold boots
// timed by file stat --wait 5m, and a root-owned 0600 proof under
// /run/spire/lxd read through the files API, compared with the guest's own
// sha256. Calls go as full. The VM is deleted at the end. It fails only
// when the probe fails or the evidence is not sanitized, and with
// LAB_COLLECT=yes copies each run to docs/validation/.
func TestGL7SELinux(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	const vm, cell, image = "l7-selinux", "v-rocky-selinux", "images:rockylinux/9/cloud"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			runID := stamp + "-g-l7-selinux-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			tags := []string{"--gate", "L7", "--cell", server + "-" + cell}
			teardown := "sudo lxc delete -f " + vm + " 2>/dev/null; true"
			l.ssh(t, server, teardown)
			t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort
			note := func(text string) { l.util(t, s, append([]string{"note", text}, tags...)...) }
			inVM := func(script string) string {
				out, err := l.trySSH(server, "sudo timeout -k 5 120 lxc exec "+vm+" -- sh -c "+shellQuote(script))
				if err != nil {
					return "failed: " + inline(out+" "+err.Error())
				}
				return strings.TrimSpace(out)
			}
			// running starts the VM when a reboot left it stopped (the
			// relabel boot ends powered off), and waits for lxd-agent
			running := func(d time.Duration) error {
				deadline := time.Now().Add(d)
				for time.Now().Before(deadline) {
					if st, _ := l.trySSH(server, "sudo lxc list "+vm+" -c s -f csv"); strings.TrimSpace(st) == "STOPPED" {
						l.trySSH(server, "sudo lxc start "+vm) //nolint:errcheck // retried
					}
					if _, err := l.trySSH(server, "sudo timeout -k 5 10 lxc exec "+vm+" -- true"); err == nil {
						return nil
					}
					time.Sleep(5 * time.Second)
				}
				return fmt.Errorf("%s: lxd-agent does not answer after %s", vm, d)
			}

			if _, err := l.trySSH(server, "sudo timeout 20m lxc launch "+image+" "+vm+" --vm"); err != nil {
				t.Fatalf("launch: %v", err)
			}
			if err := running(5 * time.Minute); err != nil {
				t.Fatal(err)
			}
			note(fmt.Sprintf("session-only VM %s from %s (%s); as shipped: selinux %s", vm, image,
				inVM(". /etc/os-release; echo $PRETTY_NAME"), inVM("getenforce")))
			setup := inVM(`for t in 1 2 3 4 5; do dnf -y -q install selinux-policy-targeted audit >/dev/null 2>&1 && break; sleep 15; done
rpm -q selinux-policy-targeted audit >/dev/null || { echo "dnf failed"; exit 1; }
grep -q '^SELINUX=enforcing' /etc/selinux/config || { echo "not enforcing"; exit 1; }
mkdir -p /etc/systemd/system/lxd-agent.service.d
cat > /etc/systemd/system/lxd-agent.service.d/selinux.conf <<'X'
` + selinuxDropIn + `X
touch /.autorelabel
systemctl enable auditd >/dev/null 2>&1
sync; echo "$(rpm -q selinux-policy-targeted audit | paste -sd' '); SELINUX=enforcing; drop-in installed; relabel requested"`)
			if strings.HasPrefix(setup, "failed") || strings.Contains(setup, "dnf failed") || strings.Contains(setup, "not enforcing") {
				t.Fatalf("setup: %s", setup)
			}
			note("setup: " + setup)
			// the relabel boot, then the first enforcing boot
			l.ssh(t, server, "sudo lxc restart --force "+vm)
			time.Sleep(30 * time.Second)
			deadline := time.Now().Add(20 * time.Minute)
			for {
				if err := running(time.Until(deadline)); err != nil {
					note("the agent did not come up under enforcing: " + err.Error())
					t.Fatal(err)
				}
				if inVM("getenforce") == "Enforcing" {
					break
				}
				time.Sleep(10 * time.Second)
			}
			// the control: one cold boot whose first agent start skips the
			// drop-in (set now: the relabel boot would consume it)
			inVM("touch /etc/lxd-probe-l7-skip-once && sync") // a forced restart loses what is not on disk
			l.ssh(t, server, "sudo lxc restart --force "+vm)
			if err := running(5 * time.Minute); err != nil {
				t.Fatal(err)
			}
			control := inVM(`echo "first start: $(journalctl -b -u lxd-agent --no-pager | grep -m1 'Failed listening on vsock' | sed 's/.*Error: //')"; ausearch -m avc -ts boot 2>/dev/null | grep -m1 -oE 'denied .* tclass=[a-z_]+' | sed -E 's/pid=[0-9]+ //'; echo "starts this boot: $(journalctl -b -u lxd-agent --no-pager | grep -c 'msg=Starting')"`)
			note("control (drop-in skipped once): " + inline(control))
			state := inVM(selinuxStateScript)
			note("with the drop-in: " + state)
			t.Logf("  control: %s", inline(control))
			t.Logf("  with the drop-in: %s", state)

			const path = "/etc/hostname"
			for i := 1; i <= 3; i++ {
				l.ssh(t, server, "sudo lxc restart --force "+vm) // a cold boot each time
				w := l.probe(t, s, server, "full", append([]string{"server", "file", "stat", vm, path, "--wait", "5m"}, tags...)...)
				ready, _ := w.observation("wait.ready_after_ms")
				attempts, _ := w.observation("wait.attempts")
				t.Logf("  start %d: files API after %v ms, %v attempts, failures %s", i, ready, attempts, failureView(w))
			}
			if err := running(5 * time.Minute); err != nil {
				t.Fatal(err)
			}
			note("after three cold boots: " + inVM(selinuxStateScript))

			// a proof as the attestor writes it, read through the files API
			sum := inVM(`install -d -m 0700 /run/spire/lxd && head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > /run/spire/lxd/proof-l7 && chmod 0600 /run/spire/lxd/proof-l7 && sha256sum /run/spire/lxd/proof-l7 | cut -d' ' -f1`)
			stat := l.probe(t, s, server, "full", append([]string{"server", "file", "stat", vm, "/run/spire/lxd/proof-l7"}, tags...)...)
			get := l.probe(t, s, server, "full", append([]string{"server", "file", "get", vm, "/run/spire/lxd/proof-l7"}, tags...)...)
			got, _ := get.observation("file.sha256")
			note(fmt.Sprintf("proof under /run/spire/lxd (root, 0600) read through the files API: %s; sha256 matches the guest's: %v; %s",
				fileView(stat), fmt.Sprint(got) == sum, inVM(selinuxStateScript)))
			t.Logf("  proof: %s; sha256 match %v", fileView(stat), fmt.Sprint(got) == sum)

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL7)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

//go:build lab

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ruleL7 is gate L7's decision rule (probe spec, Gate runbooks).
const ruleL7 = "L7 decision: the agent-start-order documentation for operators; which image families are supported; " +
	"Windows guests stay untested and unsupported."

// TestGL7Agent is G-L7, gate L7's session, one run per server: for each
// baseline VM, server file stat /etc/hostname once while it is stopped
// (Q2), then three times a restart and server file stat --wait 5m as
// files, which times the files API from the restart, and guest preflight.
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
				l.ssh(t, server, "sudo lxc stop "+vm)
				stopped := l.probe(t, s, server, "files", append([]string{"server", "file", "stat", vm, path}, tags...)...)
				status, _ := stopped.observation("file.http_status")
				msg, _ := stopped.observation("file.error")
				t.Logf("  %-9s stopped: %v %v", vm, status, msg)
				l.ssh(t, server, "sudo lxc start "+vm)
				for i := 1; i <= 3; i++ {
					if i > 1 {
						l.ssh(t, server, "sudo lxc restart "+vm)
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

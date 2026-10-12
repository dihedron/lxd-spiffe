//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleL14 is gate L14's decision rules (probe spec, Gate runbooks), in short.
const ruleL14 = "L14 decision: PATCH honours If-Match (412 on stale) → FR-S14 uses PATCH, else PUT of the whole " +
	"object with If-Match; no way to remove a single key but PUT → key cleanup (FR-S13) uses PUT; spurious 412 " +
	"rate in the race over a few percent → raise the retry budget and jitter; synchronous or asynchronous writes " +
	"and the wait duration → default proof_timeout."

// TestGL14ConfigWrites is G-L14, gate L14's session: as edit in a container
// and a VM of each server, the L14 runbook on user.lxd-probe.* keys (set
// with patch and auto, waiting; patch and put with a stale ETag; unset
// each way; a race of 8 writers), and once as files a set, for the 403.
// One run per server and instance; its keys are removed at the end. It
// prints what each command got, fails only when the probe fails or the
// evidence is not sanitized, and with LAB_COLLECT=yes copies each run to
// docs/validation/ and its fixtures to test/fixtures/<version>/config/<instance>/.
func TestGL14ConfigWrites(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	const key, raceKey = "user.lxd-probe.l14", "user.lxd-probe.l14-race"
	for _, server := range l.servers() {
		for _, name := range []string{"c-unpriv", "v-ubuntu"} {
			t.Run(server+"/"+name, func(t *testing.T) {
				runID := stamp + "-g-l14-" + server + "-" + name
				s := session{evidenceDir: l.evidence, runID: runID}
				uuid := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get "+name+" volatile.uuid"))
				common := []string{"--gate", "L14", "--cell", server + "-" + name,
					"--record", filepath.Join(l.evidence, runID, "fixtures"), "--allow-write", "--lab"}
				edit := func(args ...string) *run {
					return l.probe(t, s, server, "edit", append(append([]string{"server", "config"}, args...), common...)...)
				}
				type step struct {
					what string
					r    *run
				}
				var steps []step
				do := func(what string, args ...string) { steps = append(steps, step{what, edit(args...)}) }

				do("set patch auto --wait", "set", name, key, "1", "--method", "patch", "--if-match", "auto", "--wait", "--expect-instance-uuid", uuid)
				do("set patch stale", "set", name, key, "2", "--method", "patch", "--if-match", "stale")
				do("set put stale", "set", name, key, "3", "--method", "put", "--if-match", "stale")
				for _, m := range []string{"patch-empty", "patch-null", "put"} {
					edit("set", name, key, "x", "--wait")
					do("unset "+m, "unset", name, key, "--method", m)
				}
				do("race 8 writers", "race", name, raceKey, "--writers", "8")
				steps = append(steps, step{"set as files (403)", l.probe(t, s, server, "files",
					append([]string{"server", "config", "set", name, "user.lxd-probe.l14-denied", "x"}, common...)...)})
				// clean up: the session leaves no key behind
				edit("unset", name, raceKey, "--method", "patch-null")
				if left := l.ssh(t, server, "sudo lxc config show "+name); strings.Contains(left, "user.lxd-probe.l14") {
					t.Errorf("keys left behind on %s", name)
				}

				runDir := filepath.Join(l.evidence, runID)
				staging := filepath.Join(t.TempDir(), runID)
				l.sanitize(t, runDir, staging)
				l.sanitizeCheck(t, staging)
				t.Logf("run %s, in %s", runID, runDir)
				for _, st := range steps {
					t.Logf("  %-24s %s", st.what, l14View(st.r))
				}
				t.Log(ruleL14)
				if collect && !t.Failed() {
					l.collectRun(t, runID, staging)
					l.collectFixtures(t, server, "config", name, staging)
				}
			})
		}
	}
}

// l14View renders what a config command got: the write's status, an
// unset's result, a race's totals, or the error.
func l14View(r *run) string {
	get := func(key string) string {
		v, ok := r.observation(key)
		if !ok {
			return ""
		}
		data, _ := json.Marshal(v)
		return string(data)
	}
	parts := []string{fmt.Sprintf("exit %d, %s", r.code, r.rec.Outcome)}
	for _, key := range []string{"pre.http_status", "write.http_status", "operation.status", "unset.result", "race.totals", "race.consistent", "instance.restarted"} {
		if v := get(key); v != "" {
			parts = append(parts, key+"="+v)
		}
	}
	return strings.Join(parts, ", ")
}

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

// TestGL15FilesErrors records, for gate L15, the files API's error
// responses that the attestor handles (v0.6): on each server, with --record,
// a read as read (403), a missing file in c-unpriv and v-ubuntu as files
// (404), v-alpine stopped (500 "Instance is not running") and just started
// (500 "LXD VM agent is not currently running"), then reads right after
// its agent answers, which may catch the brief 404 of gate L7 (recorded if
// seen, not required). v-alpine runs again at the end. Fixtures go to
// test/fixtures/<version>/files-errors/baseline/ with LAB_COLLECT=yes.
func TestGL15FilesErrors(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			runID := stamp + "-g-l15-files-errors-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			fixtures := filepath.Join(l.evidence, runID, "fixtures")
			stat := func(identity, cell, name, path string, extra ...string) string {
				args := append([]string{"server", "file", "stat", name, path, "--gate", "L15", "--cell", server + "-" + cell, "--record", fixtures}, extra...)
				r := l.probe(t, s, server, identity, args...)
				status, _ := r.observation("file.http_status")
				msg, _ := r.observation("file.error")
				return fmt.Sprintf("%v %v", status, msg)
			}
			const missing = "/run/lxd-probe/no-such-file"
			t.Logf("  403 as read:          %s", stat("read", "c-unpriv", "c-unpriv", "/etc/hostname", "--expect-denied"))
			t.Logf("  404 in c-unpriv:      %s", stat("files", "c-unpriv", "c-unpriv", missing))
			t.Logf("  404 in v-ubuntu:      %s", stat("files", "v-ubuntu", "v-ubuntu", missing))
			l.ssh(t, server, "sudo lxc stop --timeout 120 v-alpine || sudo lxc stop --force v-alpine")
			t.Logf("  stopped VM:           %s", stat("files", "v-alpine", "v-alpine", "/etc/hostname"))
			l.ssh(t, server, "sudo lxc start v-alpine")
			t.Logf("  VM just started:      %s", stat("files", "v-alpine", "v-alpine", "/etc/hostname"))
			if err := l.waitAgent(server, "v-alpine"); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 3; i++ {
				t.Logf("  right after agent %d:  %s", i, stat("files", "v-alpine", "v-alpine", "/etc/hostname"))
			}

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL15)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
				l.collectFixtures(t, server, "files-errors", "baseline", staging)
			}
		})
	}
}

// TestGL15ClusterErrors records, for gate L15, the responses the attestor
// handles as a cluster forwards them: on each running cluster, through the
// third member, which holds neither baseline instance, with --record:
// server whoami (identities/current), a read as read (403), lab-missing
// (404), a missing file in c-unpriv and v-ubuntu (404), a PATCH with a
// stale ETag on both (412), and v-ubuntu stopped (500 "Instance is not
// running"), just started (500 "LXD VM agent is not currently running") and
// once its agent answers (200). v-ubuntu runs again at the end. Fixtures go
// to test/fixtures/<version>/cluster-errors/<member>/ with LAB_COLLECT=yes.
func TestGL15ClusterErrors(t *testing.T) {
	l := newLab(t)
	clusters := l.clusters()
	if len(clusters) == 0 {
		t.Skip("cluster fixtures need a cluster: LAB_CLUSTER=5.21, 6.9 or both")
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, cluster := range clusters {
		t.Run(cluster, func(t *testing.T) {
			t.Parallel()
			members := l.members(cluster)
			if len(members) < 3 {
				t.Fatalf("cluster %s has %d members, the fixtures need 3", cluster, len(members))
			}
			member := members[2]
			runID := stamp + "-g-l15-cluster-errors-" + cluster + "-" + member
			s := session{evidenceDir: l.evidence, runID: runID}
			fixtures := filepath.Join(l.evidence, runID, "fixtures")
			probe := func(identity, cell string, args ...string) *run {
				args = append(args, "--gate", "L15", "--cell", cluster+"-"+member+"-"+cell, "--record", fixtures)
				r := l.probeMember(t, s, cluster, member, identity, args...)
				if r.rec.Server == nil || r.rec.Server.Member != member {
					t.Errorf("the record names member %+v, not %s", r.rec.Server, member)
				}
				return r
			}
			stat := func(identity, name, path string, extra ...string) string {
				r := probe(identity, name, append([]string{"server", "file", "stat", name, path}, extra...)...)
				return fmt.Sprintf("%v %v", obs(r, "file.http_status"), obs(r, "file.error"))
			}
			stale := func(name string) string {
				return l14View(probe("edit", name, "server", "config", "set", name, "user.lxd-probe.l15", "x",
					"--method", "patch", "--if-match", "stale", "--allow-write", "--lab"))
			}
			const missing = "/run/lxd-probe/no-such-file"
			t.Logf("through %s:", member)
			t.Logf("  whoami as files:      %s", probe("files", "whoami", "server", "whoami").rec.Outcome)
			t.Logf("  403 as read:          %s", stat("read", "c-unpriv", "/etc/hostname", "--expect-denied"))
			t.Logf("  404 lab-missing:      %s", obs(probe("full", "lab-missing", "server", "instance", "show", "lab-missing"), "instance.http_status"))
			t.Logf("  404 in c-unpriv:      %s", stat("files", "c-unpriv", missing))
			t.Logf("  404 in v-ubuntu:      %s", stat("files", "v-ubuntu", missing))
			t.Logf("  412 on c-unpriv:      %s", stale("c-unpriv"))
			t.Logf("  412 on v-ubuntu:      %s", stale("v-ubuntu"))
			l.ssh(t, members[0], "sudo lxc stop --timeout 120 v-ubuntu || sudo lxc stop --force v-ubuntu")
			t.Logf("  stopped VM:           %s", stat("files", "v-ubuntu", "/etc/hostname"))
			l.ssh(t, members[0], "sudo lxc start v-ubuntu")
			t.Logf("  VM just started:      %s", stat("files", "v-ubuntu", "/etc/hostname"))
			if err := l.waitAgent(members[0], "v-ubuntu"); err != nil {
				t.Fatal(err)
			}
			t.Logf("  agent answers:        %s", stat("files", "v-ubuntu", "/etc/hostname"))
			if left := l.ssh(t, members[0], "sudo lxc config show c-unpriv; sudo lxc config show v-ubuntu"); strings.Contains(left, "user.lxd-probe.l15") {
				t.Errorf("user.lxd-probe.l15 left behind: a stale PATCH was applied")
			}

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Logf("LXD %s", l.env.Servers[cluster].Version)
			t.Log(ruleL15)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
				l.collectFixtures(t, cluster, "cluster-errors", member, staging)
			}
		})
	}
}

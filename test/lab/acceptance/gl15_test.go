//go:build lab

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
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

//go:build lab

package acceptance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
)

// rehearsalInstances are the baseline instances the rehearsals run in.
var rehearsalInstances = []string{"c-unpriv", "c-priv", "c-iso", "c-raw", "v-ubuntu", "v-debian", "v-alpine"}

// spireWrite are the guest flags that allow writes under /run/spire/lxd.
var spireWrite = []string{"--allow-write", "--lab", "--allow-spire-paths"}

// TestR1RehearseFilePull is R-1: on each server, server rehearse file-pull
// as files, piped to guest rehearse file-pull as root, in every baseline
// instance, then guest file rm of the proof; once per server a negative
// rehearsal with --tamper and --expect-fail compare. c-priv needs the
// attestor's allow_privileged, which the session passes to rehearse the
// proof itself. It fails on any unexpected outcome (--strict).
func TestR1RehearseFilePull(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-r-1-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			type rcase struct {
				instance        string
				negative        bool
				serverX, guestX []string
				want            string
			}
			cases := []rcase{}
			for _, inst := range rehearsalInstances {
				cases = append(cases, rcase{instance: inst, want: "passed"})
			}
			cases = append(cases, rcase{instance: "c-unpriv", negative: true,
				serverX: []string{"--expect-fail", "compare"}, guestX: []string{"--tamper"}, want: "failed at compare, as expected"})
			for _, c := range cases {
				cell := []string{"--cell", server + "-" + c.instance}
				if c.negative {
					cell[1] += "-tamper"
				}
				sargs := append(append([]string{"--strict", "--allow-privileged"}, cell...), c.serverX...)
				gargs := append(append(append([]string{}, spireWrite...), cell...), c.guestX...)
				reh := l.rehearse(t, s, server, "files", "default", c.instance, rehearse.FilePull, sargs, gargs)
				if reh.server.code != 0 || reh.server.result() != c.want {
					t.Errorf("%s %s: %q, want %q: %s", server, cell[1], reh.server.result(), c.want, reh.server)
				}
				t.Logf("  %-26s %s", cell[1], reh.server.result())
				rm := append([]string{"guest", "file", "rm", reh.challenge.ProofPath}, append(spireWrite, cell...)...)
				l.guest(t, s, server, "default", c.instance, rm...)
			}
			for _, inst := range rehearsalInstances {
				l.pull(t, server, "default", inst, runID)
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// TestR2RehearseConfigPush is R-2: the same instances, server rehearse
// config-push as edit with --allow-spire-keys, piped to guest rehearse
// config-push as root; afterwards server config get shows the key gone;
// once per server --tamper and --expect-fail compare, the key gone too.
func TestR2RehearseConfigPush(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-r-2-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			type rcase struct {
				instance        string
				negative        bool
				serverX, guestX []string
				want            string
			}
			cases := []rcase{}
			for _, inst := range rehearsalInstances {
				cases = append(cases, rcase{instance: inst, want: "passed"})
			}
			cases = append(cases, rcase{instance: "c-unpriv", negative: true,
				serverX: []string{"--expect-fail", "compare"}, guestX: []string{"--tamper"}, want: "failed at compare, as expected"})
			for _, c := range cases {
				cell := []string{"--cell", server + "-" + c.instance}
				if c.negative {
					cell[1] += "-tamper"
				}
				sargs := append(append([]string{"--strict", "--allow-privileged", "--allow-write", "--lab", "--allow-spire-keys"}, cell...), c.serverX...)
				gargs := append(append([]string{}, cell...), c.guestX...)
				reh := l.rehearse(t, s, server, "edit", "default", c.instance, rehearse.ConfigPush, sargs, gargs)
				if reh.server.code != 0 || reh.server.result() != c.want {
					t.Errorf("%s %s: %q, want %q: %s", server, cell[1], reh.server.result(), c.want, reh.server)
				}
				get := l.probe(t, s, server, "edit", append([]string{"server", "config", "get", c.instance, reh.challenge.ProofKey}, cell...)...)
				if present, _ := get.observation("config.present"); present != false {
					t.Errorf("%s %s: %s still set: %s", server, cell[1], reh.challenge.ProofKey, get)
				}
				t.Logf("  %-26s %s, key gone", cell[1], reh.server.result())
			}
			for _, inst := range rehearsalInstances {
				l.pull(t, server, "default", inst, runID)
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

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

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// ruleL1 is gate L1's decision rules (probe spec, Gate runbooks), in short.
const ruleL1 = "L1 decision: header names found → the attestor spec's minimal API contract gets them (remove [V:L1]); " +
	"owner reported as the in-container uid → file_pull's simple rule, UID translation a fallback; as the host uid → " +
	"UID translation is normative; symlink or directory not distinguishable → file_pull needs another type check. " +
	"The idmap cross-check (util idmap, chunk 9) runs offline on these records."

// l1Paths are the paths gate L1 asks the files API about.
var l1Paths = []string{
	"/run/lxd-probe/proof",
	"/run/lxd-probe/layout/regular",
	"/run/lxd-probe/layout/symlink",
	"/run/lxd-probe/layout/directory",
	"/run/lxd-probe/layout/world-writable",
	"/run/lxd-probe/layout/other-owner",
}

// TestGL1FilesAPI is G-L1, gate L1's session: in each cell, as root in the
// guest, preflight, idmap show, the hostile layouts and a proof; then, as
// files, instance show and the files API on each path (stat, plus get,
// head and dir), with --record, and once as read for the 403. One run per
// server and instance holds both environments. It prints what the guest
// sees next to what LXD reports, fails only when the probe fails or the
// evidence is not sanitized, and with LAB_COLLECT=yes copies each run to
// docs/validation/ and its fixtures to test/fixtures/<version>/files/<instance>/.
func TestGL1FilesAPI(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		for _, name := range []string{"c-unpriv", "c-priv", "c-iso", "c-raw", "v-ubuntu", "v-debian", "v-alpine"} {
			t.Run(server+"/"+name, func(t *testing.T) {
				runID := stamp + "-g-l1-" + server + "-" + name
				cell := server + "-" + name
				s := session{evidenceDir: l.evidence, runID: runID}
				tags := []string{"--gate", "L1", "--cell", cell}

				// the guest: as root, a fresh proof and layout
				l.ssh(t, server, "sudo lxc exec "+name+" -- rm -f /run/lxd-probe/proof")
				l.guest(t, s, server, "default", name, append([]string{"guest", "preflight"}, tags...)...)
				l.guest(t, s, server, "default", name, append([]string{"guest", "idmap", "show"}, tags...)...)
				layout := l.guest(t, s, server, "default", name, append([]string{"guest", "file", "layout", "--allow-write", "--lab"}, tags...)...)
				proof := l.guest(t, s, server, "default", name, append([]string{"guest", "file", "put", "/run/lxd-probe/proof",
					"--content-file", "/proc/sys/kernel/random/uuid", "--allow-write", "--lab"}, tags...)...)

				// the server: as files, with --record
				rec := append(tags, "--record", filepath.Join(l.evidence, runID, "fixtures"))
				l.probe(t, s, server, "files", append([]string{"server", "instance", "show", name}, rec...)...)
				reported := map[string]*run{}
				for _, p := range l1Paths {
					reported[p] = l.probe(t, s, server, "files", append([]string{"server", "file", "stat", name, p}, rec...)...)
				}
				l.probe(t, s, server, "files", append([]string{"server", "file", "get", name, l1Paths[0]}, rec...)...)
				l.probe(t, s, server, "files", append([]string{"server", "file", "head", name, l1Paths[0]}, rec...)...)
				l.probe(t, s, server, "files", append([]string{"server", "file", "dir", name, "/run/lxd-probe/layout"}, rec...)...)
				denied := l.probe(t, s, server, "read", append([]string{"server", "file", "stat", name, l1Paths[0], "--expect-denied"}, tags...)...)
				if denied.rec.Outcome != evidence.OutcomeOK {
					t.Logf("finding: read on the files API: %s", denied)
				}

				l.pull(t, server, "default", name, runID)
				runDir := filepath.Join(l.evidence, runID)
				staging := filepath.Join(t.TempDir(), runID)
				l.sanitize(t, runDir, staging)
				l.sanitizeCheck(t, staging)
				t.Logf("run %s, in %s", runID, runDir)
				t.Logf("  %-38s %-26s %s", "path", "guest (type mode uid:gid)", "LXD (X-LXD-type mode uid:gid)")
				for _, p := range l1Paths {
					t.Logf("  %-38s %-26s %s", p, guestView(layout, proof, p), lxdView(reported[p]))
				}
				t.Log(ruleL1)
				if collect && !t.Failed() {
					l.collectRun(t, runID, staging)
					l.collectFixtures(t, server, "files", name, staging)
				}
			})
		}
	}
}

// guestView renders what the guest recorded for p: the proof's file.stat,
// or the layout item's layout.<name>.
func guestView(layout, proof *run, p string) string {
	var v any
	var ok bool
	if p == l1Paths[0] {
		v, ok = proof.observation("file.stat")
	} else {
		v, ok = layout.observation("layout." + filepath.Base(p))
	}
	if !ok {
		return "-"
	}
	var st struct {
		Type string `json:"type"`
		Mode string `json:"mode"`
		UID  int    `json:"uid"`
		GID  int    `json:"gid"`
	}
	data, _ := json.Marshal(v)
	if json.Unmarshal(data, &st) != nil || st.Type == "" {
		return fmt.Sprint(v)
	}
	return fmt.Sprintf("%s %s %d:%d", st.Type, st.Mode, st.UID, st.GID)
}

// lxdView renders the X-LXD-* headers the files API returned.
func lxdView(r *run) string {
	v, ok := r.observation("file.headers")
	if !ok {
		return "-"
	}
	var h map[string][]string
	data, _ := json.Marshal(v)
	if json.Unmarshal(data, &h) != nil {
		return fmt.Sprint(v)
	}
	get := func(name string) string {
		for k, vals := range h {
			if strings.EqualFold(k, name) && len(vals) > 0 {
				return vals[0]
			}
		}
		return "?"
	}
	return fmt.Sprintf("%s %s %s:%s", get("X-LXD-type"), get("X-LXD-mode"), get("X-LXD-uid"), get("X-LXD-gid"))
}

// collectFixtures replaces test/fixtures/<version>/<subdir>/<instance>/
// with the run's server fixtures (its server's version only).
func (l *lab) collectFixtures(t *testing.T, server, subdir, name, staging string) {
	t.Helper()
	version := l.env.Servers[server].Version
	if version == "" || strings.ContainsAny(version, `/\`) {
		t.Fatalf("server %s: no usable version in env.json: %q", server, version)
	}
	src := filepath.Join(staging, "fixtures", version)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("no fixtures for %s: %v", version, err)
	}
	dst := filepath.Join(l.repo, "test", "fixtures", version, subdir, name)
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst, nil); err != nil {
		t.Fatalf("collecting the fixtures: %v", err)
	}
	l.sanitizeCheck(t, dst)
	t.Logf("collected: %s", dst)
}

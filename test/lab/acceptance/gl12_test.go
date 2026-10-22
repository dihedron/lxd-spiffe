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

// ruleL12 is gate L12's decision rule (probe spec, Gate runbooks).
const ruleL12 = "L12 decision: the overlap check is complete enough or must additionally forbid listed device types; " +
	"nested LXD inside a container is out of scope unless shown to defeat the check."

// l12ProofDir is where the lab's proof lives (the probe's lab directory).
const l12ProofDir = "/run/lxd-probe"

// l12Cell is one L12 cell: devices that share storage between the proof
// instance (A) and writers, set up and torn down with lxc on the server.
type l12Cell struct {
	name    string
	a       string   // the instance whose proof is forged
	writers []string // instances that write the proof
	proof   string   // the path a writer writes, as A sees it
	setup   []string // lxc commands (without "lxc") that build the cell
	// restart A after setup, so that its devices are present at boot; the
	// writers are not restarted: systemd's /run would hide their mounts
	restart bool
	cleanup []string // lxc commands that undo setup; errors ignored
}

// The session stops its copies with --force: a container whose /run is a
// host directory takes minutes to shut down cleanly.

// TestGL12SharedDevices is G-L12, gate L12's session, one run per server:
// copies of c-unpriv (A, B), c-priv (P) and v-ubuntu (V) made for the
// session share storage over the proof directory in the cells of the
// L12 runbook. In each cell a writer puts the proof with guest file put,
// A's file is read with server file stat as full (the session's instances
// are granted to no other identity), and util overlap runs on the instance
// show records of A and the writer. The session deletes what it creates.
func TestGL12SharedDevices(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	host := map[string]string{"host": "/srv/l12-host", "run": "/srv/l12-run"}
	cells := []l12Cell{
		{name: "hostdir-hotplug", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/proof",
			setup: []string{
				"config device add l12-a l12 disk source=" + host["host"] + " path=" + l12ProofDir + " shift=true",
				"config device add l12-b l12 disk source=" + host["host"] + " path=" + l12ProofDir + " shift=true",
			}},
		// the same devices at boot: systemd mounts its /run over them
		{name: "hostdir-boot", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/proof", restart: true,
			cleanup: []string{"config device remove l12-a l12", "config device remove l12-b l12"}},
		{name: "volume", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/proof",
			setup: []string{
				"storage volume create default l12-vol",
				"storage volume attach default l12-vol l12-a " + l12ProofDir,
				"storage volume attach default l12-vol l12-b " + l12ProofDir,
			},
			cleanup: []string{
				"storage volume detach default l12-vol l12-a", "storage volume detach default l12-vol l12-b",
				"storage volume delete default l12-vol",
			}},
		{name: "profile", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/proof",
			setup: []string{
				"profile create l12-shared",
				"profile device add l12-shared l12 disk source=" + host["host"] + " path=" + l12ProofDir + " shift=true",
				"profile add l12-a l12-shared", "profile add l12-b l12-shared",
			},
			cleanup: []string{"profile remove l12-a l12-shared", "profile remove l12-b l12-shared", "profile delete l12-shared"}},
		{name: "below", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/sub/proof",
			setup: []string{
				"config device add l12-a l12 disk source=" + host["host"] + " path=" + l12ProofDir + "/sub shift=true",
				"config device add l12-b l12 disk source=" + host["host"] + " path=" + l12ProofDir + "/sub shift=true",
			},
			cleanup: []string{"config device remove l12-a l12", "config device remove l12-b l12"}},
		{name: "ancestor", a: "l12-a", writers: []string{"l12-b"}, proof: l12ProofDir + "/proof",
			setup: []string{
				"stop --force l12-a l12-b",
				"config device add l12-a l12 disk source=" + host["run"] + " path=/run shift=true",
				"config device add l12-b l12 disk source=" + host["run"] + " path=/run shift=true",
				"start l12-a l12-b",
			},
			cleanup: []string{"stop --force l12-a l12-b", "config device remove l12-a l12", "config device remove l12-b l12", "start l12-a l12-b"}},
		{name: "vm-volume", a: "l12-v", writers: []string{"l12-b", "l12-p"}, proof: l12ProofDir + "/proof",
			setup: []string{
				"storage volume create default l12-vmvol security.shifted=true",
				"storage volume attach default l12-vmvol l12-v " + l12ProofDir,
				"storage volume attach default l12-vmvol l12-b " + l12ProofDir,
				"storage volume attach default l12-vmvol l12-p " + l12ProofDir,
			},
			restart: true,
			cleanup: []string{
				"storage volume detach default l12-vmvol l12-v", "storage volume detach default l12-vmvol l12-b",
				"storage volume detach default l12-vmvol l12-p", "storage volume delete default l12-vmvol",
			}},
	}
	teardown := "for i in l12-a l12-b l12-p l12-v; do lxc delete -f $i 2>/dev/null; done; " +
		"for v in l12-vol l12-vmvol; do lxc storage volume delete default $v 2>/dev/null; done; " +
		"lxc profile delete l12-shared 2>/dev/null; rm -rf " + host["host"] + " " + host["run"] + "; true"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l12-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			lxc := func(command string) error {
				_, err := l.trySSH(server, "sudo lxc "+command)
				return err
			}
			l.ssh(t, server, "sudo sh -c '"+teardown+"'")
			t.Cleanup(func() { l.trySSH(server, "sudo sh -c '"+teardown+"'") }) //nolint:errcheck // best effort
			for _, c := range []string{
				"mkdir -p " + host["host"] + " " + host["run"],
				"lxc copy c-unpriv l12-a", "lxc copy c-unpriv l12-b", "lxc copy c-priv l12-p", "lxc copy v-ubuntu l12-v",
				"lxc start l12-a l12-b l12-p",
			} {
				l.ssh(t, server, "sudo "+c)
			}
			l.util(t, s, "note", "nested LXD inside a container is out of scope (L12 runbook): not tested", "--gate", "L12", "--cell", server+"-nested")
			wrote := map[string]bool{}
			t.Logf("  %-16s %-7s %-34s %-24s %s", "cell", "writer", "A's file (status, owner, sha256)", "the writer's own write", "overlap of A")
			for _, c := range cells {
				cell := server + "-" + c.name
				tags := []string{"--gate", "L12", "--cell", cell}
				var failed string
				for _, cmd := range c.setup {
					if err := lxc(cmd); err != nil {
						failed = fmt.Sprintf("lxc %s: %v", cmd, err)
						break
					}
				}
				if failed == "" && c.restart {
					// a stopped A (a fresh copy) is started instead
					if err := lxc("restart --force " + c.a); err != nil {
						if err := lxc("start " + c.a); err != nil {
							failed = fmt.Sprintf("restart %s: %v", c.a, err)
						}
					}
				}
				if failed == "" {
					for _, i := range append([]string{c.a}, c.writers...) {
						if err := l.waitAgent(server, i); err != nil {
							failed = err.Error()
						}
					}
				}
				if failed != "" {
					l.util(t, s, "note", "cell "+c.name+" could not be built: "+failed, "--gate", "L12", "--cell", cell)
					t.Logf("  %-16s not built: %s", c.name, failed)
				} else {
					for _, w := range c.writers {
						// the previous proof, from either side: A may not see the shared one
						for _, i := range []string{c.a, w} {
							l.trySSH(server, "sudo timeout -k 5 20 lxc exec "+i+" -- rm -f "+c.proof) //nolint:errcheck // may not exist
						}
						wrote[w] = true
						put := l.guest(t, s, server, "default", w, append([]string{"guest", "file", "put", c.proof,
							"--content-file", "/proc/sys/kernel/random/uuid", "--allow-write", "--lab"}, tags...)...)
						stat := l.probe(t, s, server, "full", append([]string{"server", "file", "stat", c.a, c.proof}, tags...)...)
						showA := l.probe(t, s, server, "full", append([]string{"server", "instance", "show", c.a}, tags...)...)
						showW := l.probe(t, s, server, "full", append([]string{"server", "instance", "show", w}, tags...)...)
						overlap := l.util(t, s, append([]string{"overlap", "--proof-dir", l12ProofDir,
							recordPath(l.evidence, runID, showA), recordPath(l.evidence, runID, showW)}, tags...)...)
						t.Logf("  %-16s %-7s %-34s %-24s %s", c.name, w, fileView(stat), putView(put), overlapView(overlap))
					}
				}
				for _, cmd := range c.cleanup {
					lxc(cmd) //nolint:errcheck // undo what was built
				}
			}
			for _, w := range []string{"l12-b", "l12-p"} {
				if wrote[w] {
					l.pull(t, server, "default", w, runID)
				}
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL12)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// waitAgent waits until lxc exec works in the instance (a VM's lxd-agent
// answers), each try bounded: an exec started too early can hang.
func (l *lab) waitAgent(server, instance string) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := l.trySSH(server, "sudo timeout -k 5 10 lxc exec "+instance+" -- true"); err == nil {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("%s does not answer lxc exec after 5 minutes", instance)
}

// recordPath is the file of a server record of the run.
func recordPath(evidenceDir, runID string, r *run) string {
	env, n, _ := strings.Cut(r.rec.ID, "/")
	matches, _ := filepath.Glob(filepath.Join(evidenceDir, runID, env, "records", n+"-*.json"))
	if len(matches) != 1 {
		return filepath.Join(evidenceDir, runID, env, "records", n+"-missing.json")
	}
	return matches[0]
}

func fileView(stat *run) string {
	status, _ := stat.observation("file.http_status")
	headers, _ := stat.observation("file.headers")
	sum, _ := stat.observation("file.sha256")
	h, _ := headers.(map[string]any)
	owner := "-"
	if uid, ok := h["X-Lxd-Uid"].([]any); ok && len(uid) > 0 {
		owner = fmt.Sprint(uid[0])
	}
	short := fmt.Sprint(sum)
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%v uid %s %s", status, owner, short)
}

func putView(put *run) string {
	st, ok := put.observation("file.sha256")
	if !ok {
		return string(put.rec.Outcome)
	}
	short := fmt.Sprint(st)
	if len(short) > 8 {
		short = short[:8]
	}
	return "sha256 " + short
}

func overlapView(o *run) string {
	results, _ := o.observation("overlap.results")
	list, _ := results.([]any)
	if len(list) == 0 {
		return "-"
	}
	first, _ := list[0].(map[string]any)
	causes, _ := first["causes"].([]any)
	var why []string
	for _, c := range causes {
		m, _ := c.(map[string]any)
		why = append(why, fmt.Sprintf("%v:%v", m["device"], m["relation"]))
	}
	shared, _ := o.observation("overlap.shared_sources")
	return fmt.Sprintf("%v %v shared %v", first["verdict"], why, shared)
}

//go:build lab

package acceptance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// ruleL6 is gate L6's decision rule (probe spec, Gate runbooks), in short.
const ruleL6 = "L6 decision: confirms or amends the [V:L6] statements of the attestor spec's claims about identity " +
	"and provenance; a duplicate volatile.uuid across servers confirms that the SPIFFE ID must include server_id " +
	"(FR-S7) and that T7's ambiguity rule applies; FR-S10's rollback detection (generation) is confirmed or adapted."

// l6Key is the stand-in key each copy carries (probe spec, L6 runbook).
const l6Key = "user.lxd-probe.l6-key"

// l6Identity lists the fields that may identify an instance, as the
// summary shows them.
var l6Identity = []string{"volatile.uuid", "volatile.uuid.generation", "volatile.cloud-init.instance-id"}

// TestGL6IdentityStability is G-L6, gate L6's session. From lab.sh reset
// (skipped with LAB_L6_RESET=no), for each of c-unpriv and v-ubuntu
// (LAB_L6_INSTANCES), on lxd5: a copy l6-<x> carrying a random marker in
// user.lxd-probe.l6-key and in cloud-init.user-data; instance watch on it
// for LAB_L6_WATCH (default 5m; the copy disappears under its name at the rename); then local copy, snapshot and restore,
// rename, copy and move to lxd6 (through an lxc remote added for the
// session), export and import on lxd6, and lxd recover on lxd6 of the
// imported instance after its database entry is removed. Each step: the
// lxc command and whether the key and the cloud-init marker travelled, as
// a util note; instance show of the source and the result, util diff
// between them, config get of the key. The copies stay stopped. Calls go
// as full (the identities' grants name the baseline instances only). Ends
// with lab.sh reset. One run per source instance; it fails only when the
// probe fails or the evidence is not sanitized, and with LAB_COLLECT=yes
// copies each run to docs/validation/.
func TestGL6IdentityStability(t *testing.T) {
	l := newLab(t)
	if len(l.servers()) < 2 {
		t.Skip("G-L6 needs two servers")
	}
	src, dst := "lxd5", "lxd6"
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	reset := setting("LAB_L6_RESET", "yes") == "yes"
	watchFor := setting("LAB_L6_WATCH", "5m")
	if reset {
		l.labsh(t, "reset")
		t.Cleanup(func() {
			cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "reset")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("lab.sh reset: %v\n%s", err, out)
			}
		})
	}

	// the remote, as root on lxd5: lxd6 trusts it for the session
	token := strings.TrimSpace(l.ssh(t, dst, "sudo lxc config trust add --name l6-"+src+" --quiet"))
	l.ssh(t, src, "sudo lxc remote remove "+dst+" 2>/dev/null; sudo lxc remote add "+dst+" "+token)

	for _, base := range strings.Fields(setting("LAB_L6_INSTANCES", "c-unpriv v-ubuntu")) {
		t.Run(base, func(t *testing.T) {
			runID := stamp + "-g-l6-" + base
			s := session{evidenceDir: l.evidence, runID: runID}
			name := "l6-" + strings.SplitN(base, "-", 2)[0]
			marker := randomHex(t)
			tags := func(server string) []string { return []string{"--gate", "L6", "--cell", server + "-" + base} }
			show := func(server, inst string) *run {
				return l.probe(t, s, server, "full", append([]string{"server", "instance", "show", inst}, tags(server)...)...)
			}
			lxc := func(server, command string) error {
				_, err := l.trySSH(server, "sudo timeout 20m lxc "+command)
				return err
			}

			for _, c := range []string{
				"copy " + base + " " + name,
				"config set " + name + " " + l6Key + " " + marker,
				"config set " + name + " cloud-init.user-data '#cloud-config\n# l6-marker " + marker + "\n'",
			} {
				if err := lxc(src, c); err != nil {
					t.Fatalf("lxc %s: %v", c, err)
				}
			}
			l.util(t, s, append([]string{"note", "copies of " + base + " named " + name + "*, kept stopped; stand-in key " +
				l6Key + " and a cloud-init.user-data marker set before the first step"}, tags(src)...)...)
			wait := l.startProbe(t, s, src, "full", append([]string{"server", "instance", "watch", name, "--for", watchFor}, tags(src)...)...)

			type step struct {
				what     string
				commands [][2]string // server, lxc command
				before   [2]string   // server, instance
				after    [2]string
			}
			tarball := "/root/" + name + ".tar"
			steps := []step{
				{what: "local copy", commands: [][2]string{{src, "copy " + name + " " + name + "-copy"}},
					before: [2]string{src, name}, after: [2]string{src, name + "-copy"}},
				{what: "snapshot and restore", commands: [][2]string{{src, "snapshot " + name + " l6-snap"}, {src, "restore " + name + " l6-snap"}},
					before: [2]string{src, name}, after: [2]string{src, name}},
				{what: "rename", commands: [][2]string{{src, "move " + name + " " + name + "-renamed"}},
					before: [2]string{src, name}, after: [2]string{src, name + "-renamed"}},
				{what: "copy to the other server", commands: [][2]string{{src, "copy " + name + "-renamed " + dst + ":" + name + "-renamed"}},
					before: [2]string{src, name + "-renamed"}, after: [2]string{dst, name + "-renamed"}},
				{what: "move to the other server", commands: [][2]string{{src, "move " + name + "-copy " + dst + ":" + name + "-moved"}},
					before: [2]string{src, name + "-copy"}, after: [2]string{dst, name + "-moved"}},
				{what: "export and import on the other server",
					commands: [][2]string{{src, "export " + name + "-renamed " + tarball + " --compression none --instance-only"},
						{"transfer", tarball}, {dst, "import " + tarball + " " + name + "-imported"}},
					before: [2]string{src, name + "-renamed"}, after: [2]string{dst, name + "-imported"}},
				{what: "recover after the database entry is removed",
					commands: [][2]string{{"recover", name + "-imported"}},
					before:   [2]string{dst, name + "-imported"}, after: [2]string{dst, name + "-imported"}},
			}
			var summary []string
			for _, st := range steps {
				if !l.exists(st.before[0], st.before[1]) {
					l.util(t, s, append([]string{"note", fmt.Sprintf("%s: not run, its source %s/%s does not exist",
						st.what, st.before[0], st.before[1])}, tags(st.before[0])...)...)
					summary = append(summary, fmt.Sprintf("  %-44s not run: no source", st.what))
					continue
				}
				beforeShow := show(st.before[0], st.before[1])
				var failed error
				var done []string
				for _, c := range st.commands {
					switch c[0] {
					case "transfer":
						failed = l.transfer(src, dst, c[1])
						done = append(done, "copy "+c[1]+" from "+src+" to "+dst)
					case "recover":
						failed = l.recover(dst, c[1])
						done = append(done, "lxd sql: delete instance "+c[1]+"; lxd recover")
					default:
						failed = lxc(c[0], c[1])
						done = append(done, c[0]+": lxc "+c[1])
					}
					if failed != nil {
						break
					}
				}
				result := "done"
				if failed != nil {
					result = "failed: " + inline(failed.Error())
				}
				if !l.exists(st.after[0], st.after[1]) {
					l.util(t, s, append([]string{"note", fmt.Sprintf("%s: %s; %s; result %s/%s does not exist",
						st.what, strings.Join(done, "; "), result, st.after[0], st.after[1])}, tags(st.after[0])...)...)
					summary = append(summary, fmt.Sprintf("  %-44s %s; no result", st.what, result))
					continue
				}
				afterShow := show(st.after[0], st.after[1])
				get := l.probe(t, s, st.after[0], "full", append([]string{"server", "config", "get", st.after[1], l6Key}, tags(st.after[0])...)...)
				diff := l.util(t, s, append([]string{"diff", recordPath(l.evidence, runID, beforeShow), recordPath(l.evidence, runID, afterShow)}, tags(st.after[0])...)...)
				keyKept, ciKept := "no", "no"
				if v, _ := l.trySSH(st.after[0], "sudo lxc config get "+st.after[1]+" "+l6Key); strings.TrimSpace(v) == marker {
					keyKept = "yes"
				}
				if v, _ := l.trySSH(st.after[0], "sudo lxc config get "+st.after[1]+" cloud-init.user-data"); strings.Contains(v, marker) {
					ciKept = "yes"
				}
				l.util(t, s, append([]string{"note", fmt.Sprintf("%s: %s; %s; result %s/%s; stand-in key kept: %s; cloud-init marker kept: %s",
					st.what, strings.Join(done, "; "), result, st.after[0], st.after[1], keyKept, ciKept)}, tags(st.after[0])...)...)
				summary = append(summary, fmt.Sprintf("  %-44s %-6s key %-3s cloud-init %-3s %s; get %s; diff %s",
					st.what, map[bool]string{true: "ok", false: "FAILED"}[failed == nil], keyKept, ciKept,
					identityChange(beforeShow, afterShow), getView(get), diffView(diff)))
				if failed != nil {
					summary = append(summary, "    "+result)
				}
			}
			watch := wait()

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			for _, line := range summary {
				t.Log(line)
			}
			t.Logf("  watch of %s on %s: %s", name, src, watchView(watch))
			t.Log(ruleL6)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// transfer copies a file from one lab VM to another, through the lab host:
// lab.sh ssh FROM cat, piped into lab.sh ssh TO tee.
func (l *lab) transfer(from, to, path string) error {
	labsh := filepath.Join(l.dir, "lab.sh")
	read := exec.Command(labsh, "ssh", from, "sudo cat "+path)
	write := exec.Command(labsh, "ssh", to, "sudo tee "+path+" >/dev/null")
	pipe, err := read.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	read.Stderr, write.Stdin, write.Stderr = &stderr, pipe, &stderr
	if err := read.Start(); err != nil {
		return err
	}
	werr := write.Run()
	rerr := read.Wait()
	if err := errors.Join(rerr, werr); err != nil {
		return fmt.Errorf("transfer %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// recover removes an instance's database entries (its instance and its
// storage volume rows), leaving its storage, and runs lxd recover,
// answering its questions on LXD 6: scan, recover.
func (l *lab) recover(server, name string) error {
	for _, q := range []string{
		"DELETE FROM instances WHERE name = '%[1]s' AND project_id = (SELECT id FROM projects WHERE name = 'default')",
		"DELETE FROM storage_volumes WHERE (name = '%[1]s' OR name LIKE '%[1]s/%%') AND type IN (0, 3) AND project_id = (SELECT id FROM projects WHERE name = 'default')",
	} {
		if _, err := l.trySSH(server, `sudo lxd sql global "`+fmt.Sprintf(q, name)+`"`); err != nil {
			return fmt.Errorf("removing the database entries: %w", err)
		}
	}
	out, err := l.trySSH(server, "printf 'yes\\nyes\\n' | sudo timeout 5m lxd recover")
	if err == nil && !strings.Contains(out, "Starting recovery") {
		err = errors.New("nothing recovered")
	}
	if err != nil {
		return fmt.Errorf("lxd recover: %w: %s", err, inline(out))
	}
	return nil
}

// inline puts command output on one line.
func inline(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// exists tells whether an instance exists on a server.
func (l *lab) exists(server, name string) bool {
	_, err := l.trySSH(server, "sudo lxc config show "+name+" >/dev/null")
	return err == nil
}

// startProbe starts a lab.sh probe command in the background and returns
// a function that waits for it and returns its run (nil rec on failure).
func (l *lab) startProbe(t *testing.T, s session, server, identity string, args ...string) func() *run {
	t.Helper()
	return l.startProbeMember(t, s, server, "", identity, args...)
}

// startProbeMember is startProbe through a cluster member (none: the first).
func (l *lab) startProbeMember(t *testing.T, s session, server, member, identity string, args ...string) func() *run {
	t.Helper()
	full := []string{"probe", server}
	if member != "" {
		full = append(full, "--member", member)
	}
	full = append(full, "--as", identity, "--")
	full = append(full, args...)
	full = append(full, "--json", "--evidence-dir", s.evidenceDir)
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), full...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() *run {
		t.Helper()
		err := cmd.Wait()
		r := &run{stdout: stdout.String(), stderr: stderr.String()}
		if ee, ok := err.(*exec.ExitError); ok {
			r.code = ee.ExitCode()
		}
		var rec evidence.Record
		if json.Unmarshal(stdout.Bytes(), &rec) == nil && rec.ID != "" {
			r.rec, r.recs = &rec, []*evidence.Record{&rec}
		}
		if probeFailure(r.code) || r.rec == nil {
			t.Errorf("lab.sh %s: the probe failed: %s", strings.Join(full, " "), r)
		}
		return r
	}
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// identityChange compares the identity fields of two instance show runs.
func identityChange(before, after *run) string {
	config := func(r *run) map[string]any {
		v, _ := r.observation("instance.config")
		m, _ := v.(map[string]any)
		return m
	}
	b, a := config(before), config(after)
	if a == nil {
		return "result not found"
	}
	var parts []string
	for _, k := range l6Identity {
		short := strings.TrimPrefix(k, "volatile.")
		if fmt.Sprint(b[k]) == fmt.Sprint(a[k]) {
			parts = append(parts, short+" same")
		} else {
			parts = append(parts, short+" CHANGED")
		}
	}
	return strings.Join(parts, ", ")
}

func getView(r *run) string {
	present, _ := r.observation("config.present")
	return fmt.Sprintf("exit %d present %v", r.code, present)
}

func diffView(r *run) string {
	v, ok := r.observation("diff.count")
	if !ok {
		return fmt.Sprintf("exit %d", r.code)
	}
	return fmt.Sprint(v, " differences")
}

func watchView(r *run) string {
	if r == nil || r.rec == nil {
		return "no record"
	}
	polls, _ := r.observation("watch.polls")
	changes, _ := r.observation("watch.changes")
	data, _ := json.Marshal(changes)
	return fmt.Sprintf("exit %d, polls %v, changes %s", r.code, polls, data)
}

// TestGL6SameServerImport completes G-L6: on lxd5, an export of a stopped
// copy of c-unpriv imported back on the same server under another name
// while the original exists, to learn whether LXD keeps volatile.uuid (two
// instances of one server with one uuid, hence one SPIFFE ID) or refuses
// or regenerates it. Instance show of both, util diff, a note with the
// result. It cleans up after itself; it fails only when the probe fails or
// the evidence is not sanitized, and with LAB_COLLECT=yes copies the run
// to docs/validation/.
func TestGL6SameServerImport(t *testing.T) {
	l := newLab(t)
	server, base, name := "lxd5", "c-unpriv", "l6-s"
	runID := time.Now().UTC().Format("20060102T150405Z") + "-g-l6-same-server-import-" + server
	s := session{evidenceDir: l.evidence, runID: runID}
	tags := []string{"--gate", "L6", "--cell", server + "-" + base}
	tarball := "/root/" + name + ".tar"
	teardown := "sudo lxc delete -f " + name + " 2>/dev/null; sudo lxc delete -f " + name + "-imported 2>/dev/null; sudo rm -f " + tarball + "; true"
	l.ssh(t, server, teardown)
	t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort

	for _, c := range []string{"copy " + base + " " + name, "export " + name + " " + tarball + " --compression none --instance-only"} {
		if _, err := l.trySSH(server, "sudo timeout 10m lxc "+c); err != nil {
			t.Fatalf("lxc %s: %v", c, err)
		}
	}
	original := l.probe(t, s, server, "full", append([]string{"server", "instance", "show", name}, tags...)...)
	cmd := "import " + tarball + " " + name + "-imported"
	out, err := l.trySSH(server, "sudo timeout 10m lxc "+cmd+" 2>&1")
	result := "done"
	if err != nil {
		result = "refused: " + inline(out+" "+err.Error())
	}
	view := "no imported instance"
	if l.exists(server, name+"-imported") {
		imported := l.probe(t, s, server, "full", append([]string{"server", "instance", "show", name + "-imported"}, tags...)...)
		l.util(t, s, append([]string{"diff", recordPath(l.evidence, runID, original), recordPath(l.evidence, runID, imported)}, tags...)...)
		view = identityChange(original, imported)
		// both instances still there, with their uuids
		for _, i := range []string{name, name + "-imported"} {
			uuid, _ := l.trySSH(server, "sudo lxc config get "+i+" volatile.uuid")
			view += "; " + i + " uuid " + strings.TrimSpace(uuid)
		}
	}
	l.util(t, s, append([]string{"note", fmt.Sprintf("same-server import: %s: lxc export %s %s --compression none --instance-only; lxc %s; %s; %s",
		server, name, tarball, cmd, result, view)}, tags...)...)

	runDir := filepath.Join(l.evidence, runID)
	staging := filepath.Join(t.TempDir(), runID)
	l.sanitize(t, runDir, staging)
	l.sanitizeCheck(t, staging)
	t.Logf("run %s, in %s", runID, runDir)
	t.Logf("  import of %s on %s while it exists: %s; %s", name, server, result, view)
	t.Log(ruleL6)
	if os.Getenv("LAB_COLLECT") == "yes" && !t.Failed() {
		l.collectRun(t, runID, staging)
	}
}

// TestGL6Filter learns, for the attestor's uniqueness check (gate L6),
// whether LXD finds instances by volatile.uuid through a filter
// (api_filtering) and which identities see what, one run per server:
// server instance list --filter "config.volatile.uuid eq U" as full for
// c-unpriv's uuid (one) and a random one (none); for a twin made by an
// import on the same server (two) as full, then as read, whose grants
// name the baseline instances only (the twin is invisible to it), then as
// read with the project entitlement can_view_instances, granted to
// lab-read for the session. Fixtures go to
// test/fixtures/<version>/list/filter/ with LAB_COLLECT=yes. It deletes
// what it creates and revokes the entitlement; it fails only when the probe
// fails or the evidence is not sanitized.
func TestGL6Filter(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			runID := stamp + "-g-l6-filter-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			const name, tarball = "l6-f", "/root/l6-f.tar"
			grant := "sudo lxc auth group permission %s lab-read project default can_view_instances"
			teardown := "sudo lxc delete -f " + name + " 2>/dev/null; sudo lxc delete -f " + name + "-twin 2>/dev/null; sudo rm -f " + tarball + "; " +
				fmt.Sprintf(grant, "remove") + " 2>/dev/null; true"
			l.ssh(t, server, teardown)
			t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort
			tags := []string{"--gate", "L6", "--cell", server + "-c-unpriv",
				"--record", filepath.Join(l.evidence, runID, "fixtures")}
			list := func(identity, uuid string) string {
				r := l.probe(t, s, server, identity, append([]string{"server", "instance", "list", "--filter", "config.volatile.uuid eq " + uuid}, tags...)...)
				n, _ := r.observation("instances.count")
				var names []string
				if v, ok := r.observation("instances"); ok {
					for _, e := range v.([]any) {
						names = append(names, fmt.Sprint(e.(map[string]any)["name"]))
					}
				}
				return fmt.Sprintf("exit %d, %v found %v", r.code, n, names)
			}
			note := func(text string) { l.util(t, s, "note", text, "--gate", "L6", "--cell", server+"-c-unpriv") }

			uuid := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get c-unpriv volatile.uuid"))
			results := []string{
				"c-unpriv's uuid as full: " + list("full", uuid),
				"a random uuid as full: " + list("full", "00000000-0000-4000-8000-000000000000"),
			}
			for _, c := range []string{"copy c-unpriv " + name, "export " + name + " " + tarball + " --compression none --instance-only",
				"import " + tarball + " " + name + "-twin"} {
				if _, err := l.trySSH(server, "sudo timeout 10m lxc "+c); err != nil {
					t.Fatalf("lxc %s: %v", c, err)
				}
			}
			twin := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get "+name+"-twin volatile.uuid"))
			if orig := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get "+name+" volatile.uuid")); orig != twin {
				t.Logf("  the import got a uuid of its own (%s, %s)", orig, twin)
			}
			results = append(results,
				"the twins' uuid as full: "+list("full", twin),
				"the twins' uuid as read (instance grants on the baseline only): "+list("read", twin),
				"c-unpriv's uuid as read: "+list("read", uuid))
			l.ssh(t, server, fmt.Sprintf(grant, "add"))
			results = append(results, "the twins' uuid as read with project can_view_instances: "+list("read", twin))
			l.ssh(t, server, fmt.Sprintf(grant, "remove"))
			for _, r := range results {
				note("uuid filter: " + r)
				t.Logf("  %s", r)
			}

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL6)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
				l.collectFixtures(t, server, "list", "filter", staging)
			}
		})
	}
}

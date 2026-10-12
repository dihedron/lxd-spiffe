//go:build lab

package acceptance

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The decision rules S0-1's gates end with (probe spec, Gate runbooks): the
// session prints them next to the records, and the operator decides.
const (
	ruleL8 = "L8 decision: the minimum LXD version list; the table of required extensions " +
		"(attestor spec, API usage and required extensions: projects, instance_generation_id, ...) " +
		"is confirmed, and each additional extension found necessary is added. Q3: whether LXD accepts TLS 1.2."
	ruleL15 = "L15 decision: the committed fixtures become the contract of the fake LXD API (PRT-10); " +
		"any decode failure results in a client struct fix."
)

// TestS01LabZeroServer is S0-1, the server half of Lab 0 (gates L8 and
// L15): server info, whoami and instance show for every baseline instance,
// as full, with the provoked 403 (untrusted certificate), 404 (missing
// instance) and hidden 404 (outsider in restricted), all recorded as
// fixtures. One run per server, since a run directory records one endpoint.
// It fails only when the probe fails or the evidence is not sanitized; with
// LAB_COLLECT=yes (lab.sh test --collect) it also copies the run into
// docs/validation/ and replaces the server version's fixtures under
// test/fixtures/.
func TestS01LabZeroServer(t *testing.T) {
	l := newLab(t)
	cert, key := untrustedCert(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-s0-1-" + server
			runDir := filepath.Join(l.evidence, runID)
			fixtures := filepath.Join(runDir, "fixtures")
			s := session{evidenceDir: l.evidence, runID: runID}
			var runs []*run
			record := func(gate, identity string, args ...string) {
				args = append(args, "--gate", gate, "--cell", server, "--record", fixtures)
				runs = append(runs, l.probe(t, s, server, identity, args...))
			}

			record("L8", "full", "server", "info", "--probe-tls12")
			record("L15", "full", "server", "whoami")
			for _, b := range baseline() {
				record("L15", "full", "server", "instance", "show", b.name, "--project", b.project)
			}
			record("L15", "full", "server", "instance", "show", "lab-missing")
			record("L15", "outsider", "server", "instance", "show", "c-unpriv", "--project", "restricted", "--expect-hidden")
			record("L15", "full", "server", "instance", "show", "c-unpriv", "--client-cert", cert, "--client-key", key, "--expect-denied")

			// the run directory is a working one: aliases.json maps the
			// aliases back (PRE-09). What is checked, and collected, is its
			// sanitized copy.
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			for _, r := range runs {
				t.Logf("  %s  %-4s %-14s %-40s %s", r.rec.ID, r.rec.Gate, r.rec.Verb, strings.Join(r.rec.Args, " "), r.rec.Outcome)
			}
			t.Log(ruleL8)
			t.Log(ruleL15)

			if collect && !t.Failed() {
				l.collect(t, server, runID, staging)
			}
		})
	}
	if !collect {
		t.Log("evidence kept in " + l.evidence + " only: lab.sh test --collect copies it into the repository")
	}
}

// sanitize writes the sanitized copy of the run dir to out (util sanitize
// --out), without its aliases.json.
func (l *lab) sanitize(t *testing.T, dir, out string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.state, "bin", "lxd-probe"), "util", "sanitize", dir, "--out", out, "--evidence-dir", t.TempDir())
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("util sanitize %s --out %s: %v\n%s", dir, out, err, buf.String())
	}
}

// sanitizeCheck runs util sanitize --check on dir and fails the test when
// forbidden material is found.
func (l *lab) sanitizeCheck(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.state, "bin", "lxd-probe"), "util", "sanitize", "--check", dir, "--evidence-dir", t.TempDir())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Errorf("util sanitize --check %s: %v\n%s", dir, err, out.String())
	}
}

// collect copies the sanitized run, without its fixtures, to
// docs/validation/<run_id>/, and replaces test/fixtures/<lxd-version>/ with
// its fixtures. Fixtures recorded as an untrusted client are under
// "unknown" (LXD does not tell untrusted clients its version): they belong
// to the server's version, and their record numbers do not collide.
func (l *lab) collect(t *testing.T, server, runID, staging string) {
	t.Helper()
	validation := filepath.Join(l.repo, "docs", "validation", runID)
	if err := copyTree(staging, validation, func(top string) bool { return top != "fixtures" }); err != nil {
		t.Fatalf("collecting the evidence: %v", err)
	}
	l.sanitizeCheck(t, validation)

	version := l.env.Servers[server].Version
	fixtures := filepath.Join(staging, "fixtures")
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatalf("collecting the fixtures: %v", err)
	}
	dst := filepath.Join(l.repo, "test", "fixtures", version)
	if version == "" || strings.ContainsAny(version, `/\`) {
		t.Fatalf("server %s: no usable version in env.json: %q", server, version)
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() || (e.Name() != version && e.Name() != "unknown") {
			t.Fatalf("unexpected fixtures of %s in %s: %s", server, fixtures, e.Name())
		}
		if err := copyTree(filepath.Join(fixtures, e.Name()), dst, nil); err != nil {
			t.Fatalf("collecting the fixtures: %v", err)
		}
	}
	l.sanitizeCheck(t, dst)
	t.Logf("collected: %s, %s", validation, dst)
}

// copyTree copies the regular files under src to dst, skipping the
// top-level entries keep refuses (keep nil: everything).
func copyTree(src, dst string, keep func(rel string) bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; rel != "." && keep != nil && !keep(top) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

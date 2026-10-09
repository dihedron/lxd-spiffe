package util

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func evidenceTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run, err := evidence.Open(root, "run1", "server", evidence.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.Dir, "artifacts", "0001-manual.json")
	if err := os.WriteFile(path, []byte(`{"config":{"user.x":"private"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "run1")
}

func runSanitize(t *testing.T, cmd *Sanitize) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.stdout = &out
	err := cmd.Execute(nil)
	return out.String(), err
}

func TestSanitizeCheck(t *testing.T) {
	dir := evidenceTree(t)
	cmd := &Sanitize{Check: true}
	cmd.Args.Dir = dir
	out, err := runSanitize(t, cmd)
	if exit.Code(err) != exit.Sanitize {
		t.Fatalf("exit code = %d (%v); want %d", exit.Code(err), err, exit.Sanitize)
	}
	if !strings.Contains(out, "server/artifacts/0001-manual.json") {
		t.Errorf("output does not name the file: %s", out)
	}
}

func TestSanitizeOutThenCheck(t *testing.T) {
	dir := evidenceTree(t)
	copyDir := filepath.Join(t.TempDir(), "validation", "run1")
	cmd := &Sanitize{Out: copyDir}
	cmd.Args.Dir = dir
	if _, err := runSanitize(t, cmd); err != nil {
		t.Fatal(err)
	}
	check := &Sanitize{Check: true}
	check.Args.Dir = copyDir
	check.JSON = true
	out, err := runSanitize(t, check)
	if err != nil {
		t.Fatalf("check of the copy: %v\n%s", err, out)
	}
	var report evidence.TreeReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--json output: %v\n%s", err, out)
	}
	if !report.OK() || report.Files == 0 {
		t.Errorf("report = %+v", report)
	}
}

func TestSanitizeInPlaceReminder(t *testing.T) {
	dir := evidenceTree(t)
	run, err := evidence.Open(filepath.Dir(dir), "run1", "server", evidence.RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	run.Sanitizer.RedactName(evidence.KindInstance, "c1")
	run.Sanitizer.String("c1") // allocates the alias, creating aliases.json

	cmd := &Sanitize{}
	cmd.Args.Dir = dir
	out, err := runSanitize(t, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "aliases.json") {
		t.Errorf("no reminder about aliases.json: %s", out)
	}
}

func TestSanitizeBadOut(t *testing.T) {
	dir := evidenceTree(t)
	cmd := &Sanitize{Out: filepath.Join(dir, "server", "copy")}
	cmd.Args.Dir = dir
	if _, err := runSanitize(t, cmd); exit.Code(err) != exit.Usage {
		t.Errorf("exit code = %d (%v); want %d", exit.Code(err), err, exit.Usage)
	}
}

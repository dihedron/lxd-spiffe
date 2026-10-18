package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// makeTree builds a small run with one sanitized record and some material
// that util sanitize must catch: a raw body written by hand, a note with a
// MAC address and the alias map of --redact-names.
func makeTree(t *testing.T) (root, runDir string) {
	t.Helper()
	root = t.TempDir()
	run, err := Open(root, "run1", "server", RunInfo{})
	if err != nil {
		t.Fatal(err)
	}
	run.Sanitizer.RedactName(KindInstance, "c1")
	entry, err := run.Begin("instance show")
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Commit(&Record{Time: time.Now(), Verb: "instance show", Args: []string{"c1"}, Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(run.Dir, "artifacts", "0099-manual.json"), `{"name":"c1","config":{"user.x":"private"}}`)
	write(t, filepath.Join(run.Dir, "notes.jsonl"), `{"text":"nic 00:16:3e:aa:bb:cc"}`+"\n"+`{"text":"fine"}`+"\n")
	write(t, filepath.Join(run.Dir, "artifacts", "0098-log.txt"), "line with "+testJWT+"\n")
	return root, filepath.Join(root, "run1")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeTreeCheckFails(t *testing.T) {
	_, runDir := makeTree(t)
	report, err := SanitizeTree(runDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	wantChanged := []string{
		"server/artifacts/0098-log.txt",
		"server/artifacts/0099-manual.json",
		"server/notes.jsonl",
	}
	if !slices.Equal(report.Changed, wantChanged) {
		t.Errorf("changed = %v; want %v", report.Changed, wantChanged)
	}
	if !slices.Equal(report.Forbidden, []string{"server/aliases.json"}) {
		t.Errorf("forbidden = %v", report.Forbidden)
	}
	if report.OK() {
		t.Errorf("check passed")
	}
	// check never writes
	data, _ := os.ReadFile(filepath.Join(runDir, "server", "artifacts", "0099-manual.json"))
	if !strings.Contains(string(data), "private") {
		t.Errorf("check modified a file")
	}
}

func TestSanitizeTreeInPlace(t *testing.T) {
	_, runDir := makeTree(t)
	report, err := SanitizeTree(runDir, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changed) != 3 {
		t.Errorf("changed = %v", report.Changed)
	}
	data, _ := os.ReadFile(filepath.Join(runDir, "server", "artifacts", "0099-manual.json"))
	if strings.Contains(string(data), "private") || strings.Contains(string(data), `"c1"`) {
		t.Errorf("not sanitized: %s", data)
	}
	if !strings.Contains(string(data), "instance-1") {
		t.Errorf("name not aliased with the run's map: %s", data)
	}
	assertMode(t, filepath.Join(runDir, "server", "artifacts", "0099-manual.json"), 0o600)
	// the alias map stays for the rest of the run, so --check still fails
	if _, err := os.Stat(filepath.Join(runDir, "server", "aliases.json")); err != nil {
		t.Errorf("aliases.json removed: %v", err)
	}
	report, err = SanitizeTree(runDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changed) != 0 || !slices.Equal(report.Forbidden, []string{"server/aliases.json"}) {
		t.Errorf("after in-place: changed = %v, forbidden = %v", report.Changed, report.Forbidden)
	}
}

func TestSanitizeTreeOut(t *testing.T) {
	root, runDir := makeTree(t)
	out := filepath.Join(root, "docs", "validation", "run1")
	if _, err := SanitizeTree(runDir, out, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "server", "aliases.json")); !os.IsNotExist(err) {
		t.Errorf("aliases.json copied: %v", err)
	}
	assertMode(t, filepath.Join(out, "server"), 0o700)
	assertMode(t, filepath.Join(out, "server", "records", "0001-instance-show.json"), 0o600)
	report, err := SanitizeTree(out, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Errorf("check of the copy: changed = %v, forbidden = %v", report.Changed, report.Forbidden)
	}
	// the source is untouched
	data, _ := os.ReadFile(filepath.Join(runDir, "server", "artifacts", "0099-manual.json"))
	if !strings.Contains(string(data), "private") {
		t.Errorf("--out modified the source")
	}
}

func TestSanitizeTreeOutMustBeEmpty(t *testing.T) {
	root, runDir := makeTree(t)
	out := filepath.Join(root, "out")
	write(t, filepath.Join(root, "placeholder"), "")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(out, "existing"), "x")
	if _, err := SanitizeTree(runDir, out, false); err == nil {
		t.Errorf("non-empty --out accepted")
	}
	if _, err := SanitizeTree(runDir, filepath.Join(runDir, "server", "copy"), false); err == nil {
		t.Errorf("--out inside the source accepted")
	}
}

func TestSanitizeTreeForbidden(t *testing.T) {
	_, runDir := makeTree(t)
	write(t, filepath.Join(runDir, "server", "artifacts", "0097-blob.bin"), "\xff\xfe\x00")
	if err := os.Symlink("/etc/passwd", filepath.Join(runDir, "server", "artifacts", "0096-link")); err != nil {
		t.Fatal(err)
	}
	report, err := SanitizeTree(runDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server/artifacts/0096-link", "server/artifacts/0097-blob.bin"} {
		if !slices.Contains(report.Forbidden, want) {
			t.Errorf("forbidden = %v; want %s", report.Forbidden, want)
		}
	}
}

// TestAbortedEntries: a probe killed mid-session leaves an empty record and
// its number reservation; --out leaves both out, --check ignores them, and
// the report lists them (PRE-07).
func TestAbortedEntries(t *testing.T) {
	root, runDir := makeTree(t)
	records := filepath.Join(runDir, "server", "records")
	for _, f := range []string{"0007-rehearse-file-pull.json", "0007"} {
		if err := os.WriteFile(filepath.Join(records, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(root, "docs", "validation", "run1")
	report, err := SanitizeTree(runDir, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Aborted) != 2 {
		t.Errorf("aborted %v", report.Aborted)
	}
	for _, f := range []string{"0007-rehearse-file-pull.json", "0007"} {
		if _, err := os.Stat(filepath.Join(out, "server", "records", f)); !os.IsNotExist(err) {
			t.Errorf("%s copied: %v", f, err)
		}
	}
	if check, err := SanitizeTree(runDir, "", true); err != nil || slices.Contains(check.Forbidden, "server/records/0007") {
		t.Errorf("check: %v %+v", err, check)
	}
	if _, err := ReadRecord(filepath.Join(records, "0007-rehearse-file-pull.json")); !errors.Is(err, ErrAborted) {
		t.Errorf("ReadRecord of an empty record: %v", err)
	}
	for rel, want := range map[string]bool{
		"server/records/0007-x.json": true, "server/records/0007": true, "server/records/12": true,
		"server/records/0007-x.json+": false, "server/artifacts/0007-x.json": false, "server/records/abc": false,
	} {
		if got := IsAborted(rel, 0); got != want {
			t.Errorf("IsAborted(%q) = %v", rel, got)
		}
	}
	if IsAborted("server/records/0007-x.json", 12) {
		t.Error("a record with content is not aborted")
	}
}

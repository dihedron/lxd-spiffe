package guest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// fileTest maps the guest directories under a temporary directory and
// returns it and options that allow writes.
func fileTest(t *testing.T) (string, probe.GuestCommand) {
	t.Helper()
	root := t.TempDir()
	old := hostRoot
	hostRoot = root
	t.Cleanup(func() { hostRoot = old })
	opts := probe.GuestCommand{GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir(), RunID: "r1", JSON: true, AllowWrite: true, Lab: true}}
	return root, opts
}

func contentFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func put(t *testing.T, opts probe.GuestCommand, path, text string, mode uint32) (*evidence.Record, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &FilePut{GuestCommand: opts, ContentFile: contentFile(t, text), Mode: mode, stdout: out}
	cmd.Args.Path = path
	err := cmd.Execute(nil)
	recs := records(t, out)
	if len(recs) == 0 {
		return nil, err
	}
	return &recs[0], err
}

func TestFilePut(t *testing.T) {
	root, opts := fileTest(t)
	rec, err := put(t, opts, "/run/lxd-probe/proof", "nonce-1", 0o640)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(root, "run/lxd-probe/proof"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("proof: %v, %v (the mode must not depend on the umask)", fi, err)
	}
	dir, _ := os.Lstat(filepath.Join(root, "run/lxd-probe"))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("lab directory mode %o; want 0700 (PRN-05)", dir.Mode().Perm())
	}
	if rec.Verb != "file put" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s", rec.Verb, rec.Outcome)
	}
	stat := value(t, *rec, "file.stat").(map[string]any)
	if stat["type"] != "file" || stat["mode"] != "0640" || stat["size"] != float64(7) || stat["inode"] == nil {
		t.Errorf("file.stat = %v", stat)
	}
	if jsonOf(value(t, *rec, "file.sha256")) != `"`+sha256Hex([]byte("nonce-1"))+`"` {
		t.Errorf("file.sha256 = %v", value(t, *rec, "file.sha256"))
	}
	notIn(t, "nonce-1", opts.EvidenceDir) // the content may be a proof: never recorded

	// O_EXCL: an existing file is not replaced
	if _, err := put(t, opts, "/run/lxd-probe/proof", "nonce-2", 0o600); err == nil {
		t.Error("a second put replaced the file")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "run/lxd-probe/proof")); string(data) != "nonce-1" {
		t.Errorf("proof = %q", data)
	}
}

func TestFilePutRefusals(t *testing.T) {
	root, opts := fileTest(t)
	noFlags := opts
	noFlags.AllowWrite, noFlags.Lab = false, false
	for name, tt := range map[string]struct {
		opts probe.GuestCommand
		path string
	}{
		"no --allow-write/--lab": {noFlags, "/run/lxd-probe/x"},
		"outside":                {opts, "/tmp/x"},
		"dot-dot":                {opts, "/run/lxd-probe/../x"},
		"the lab directory":      {opts, "/run/lxd-probe"},
		"SPIRE without the flag": {opts, "/run/spire/lxd/proof"},
	} {
		rec, err := put(t, tt.opts, tt.path, "x", 0o600)
		if exit.Code(err) != exit.Refused || rec != nil {
			t.Errorf("%s: exit %d (%v), record %v; want a refusal before anything is written", name, exit.Code(err), err, rec)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "run")); len(entries) != 0 {
		t.Errorf("a refused put touched %v", entries)
	}

	// a symlink planted inside the lab directory cannot lead a write out
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run/lxd-probe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "run/lxd-probe/escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := put(t, opts, "/run/lxd-probe/escape/x", "x", 0o600); err == nil {
		t.Error("a put followed a symlink out of the lab directory")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("written outside: %v", entries)
	}

	// --dry-run prints what it would do and writes nothing
	dry := opts
	dry.DryRun = true
	out := &bytes.Buffer{}
	cmd := &FilePut{GuestCommand: dry, ContentFile: contentFile(t, "x"), Mode: 0o600, stdout: out}
	cmd.Args.Path = "/run/lxd-probe/dry"
	if err := cmd.Execute(nil); err != nil || !strings.Contains(out.String(), "/run/lxd-probe/dry") {
		t.Errorf("dry run: %v, %q", err, out.String())
	}
	if _, err := os.Lstat(filepath.Join(root, "run/lxd-probe/dry")); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote the file: %v", err)
	}
}

func TestFileRm(t *testing.T) {
	root, opts := fileTest(t)
	rm := func(o probe.GuestCommand, path string) error {
		cmd := &FileRm{GuestCommand: o, stdout: &bytes.Buffer{}}
		cmd.Args.Path = path
		return cmd.Execute(nil)
	}
	if _, err := put(t, opts, "/run/lxd-probe/proof", "x", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rm(opts, "/run/lxd-probe/proof"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "run/lxd-probe/proof")); !os.IsNotExist(err) {
		t.Errorf("not removed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "run/lxd-probe/d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := rm(opts, "/run/lxd-probe/d"); exit.Code(err) != exit.Refused {
		t.Errorf("rm of a directory: %v", err)
	}

	// under the SPIRE directory, only what put marked
	spire := opts
	spire.AllowSpirePaths = true
	if _, err := put(t, spire, "/run/spire/lxd/proof", "x", 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "run/spire/lxd/.proof.lxd-probe")); err != nil {
		t.Errorf("no marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "run/spire/lxd/agent-owned"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rm(spire, "/run/spire/lxd/agent-owned"); exit.Code(err) != exit.Refused {
		t.Errorf("rm of an unmarked SPIRE file: %v", err)
	}
	if err := rm(spire, "/run/spire/lxd/proof"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"proof", ".proof.lxd-probe"} {
		if _, err := os.Lstat(filepath.Join(root, "run/spire/lxd", name)); !os.IsNotExist(err) {
			t.Errorf("%s not removed: %v", name, err)
		}
	}
	noFlags := opts
	noFlags.AllowWrite = false
	if err := rm(noFlags, "/run/lxd-probe/x"); exit.Code(err) != exit.Refused {
		t.Errorf("rm without --allow-write: %v", err)
	}
}

func TestFileStat(t *testing.T) {
	root, opts := fileTest(t)
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	opts.AllowWrite, opts.Lab = false, false // read-only
	cmd := &FileStat{GuestCommand: opts, stdout: out}
	cmd.Args.Path = filepath.Join(dir, "link") // stat reads any path, as given
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := records(t, out)[0]
	lstat := value(t, rec, "file.lstat").(map[string]any)
	if lstat["type"] != "symlink" || lstat["target"] != "/etc/hostname" {
		t.Errorf("file.lstat = %v", lstat)
	}
	value(t, rec, "file.stat") // the target's, or its error
}

func TestFileLayout(t *testing.T) {
	root, opts := fileTest(t)
	layout := func() evidence.Record {
		out := &bytes.Buffer{}
		cmd := &FileLayout{GuestCommand: opts, stdout: out}
		if err := cmd.Execute(nil); err != nil {
			t.Fatal(err)
		}
		return records(t, out)[0]
	}
	rec := layout()
	base := filepath.Join(root, "run/lxd-probe/layout")
	for name, check := range map[string]func(os.FileInfo) bool{
		"regular":        func(fi os.FileInfo) bool { return fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 },
		"symlink":        func(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 },
		"directory":      func(fi os.FileInfo) bool { return fi.IsDir() },
		"world-writable": func(fi os.FileInfo) bool { return fi.Mode().Perm() == 0o666 },
	} {
		fi, err := os.Lstat(filepath.Join(base, name))
		if err != nil || !check(fi) {
			t.Errorf("layout/%s: %v, %v", name, fi, err)
		}
		value(t, rec, "layout."+name)
	}
	if target, _ := os.Readlink(filepath.Join(base, "symlink")); target != "/etc/hostname" {
		t.Errorf("symlink -> %q; want /etc/hostname (outside the lab directory)", target)
	}
	other := value(t, rec, "layout.other-owner")
	if syscall.Geteuid() != 0 && !strings.Contains(jsonOf(other), "skipped") {
		t.Errorf("layout.other-owner as non-root = %v", other)
	}
	// a second run replaces the layout
	layout()
}

func TestIdmapShow(t *testing.T) {
	_, opts := fileTest(t)
	out := &bytes.Buffer{}
	cmd := &IdmapShow{GuestCommand: opts, stdout: out}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := records(t, out)[0]
	if rec.Verb != "idmap show" || !strings.Contains(jsonOf(value(t, rec, "idmap.uid_map")), `"inside":`) {
		t.Errorf("idmap show: %q %v", rec.Verb, rec.Observations)
	}
}

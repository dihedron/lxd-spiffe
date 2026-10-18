package evidence

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestEvidenceFilesNotThroughSymlinks: a record or alias map that is a
// symlink, or not a regular file, is refused, and the file a symlinked
// alias map points to is neither read nor written.
func TestEvidenceFilesNotThroughSymlinks(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.json")
	const content = `{"mac":{"real":"mac-1"}}`
	if err := os.WriteFile(secret, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	record := filepath.Join(dir, "0001-whoami.json")
	if err := os.Symlink(secret, record); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(record); err == nil {
		t.Error("ReadRecord followed a symlink")
	}

	aliases := filepath.Join(dir, "aliases.json")
	if err := os.Symlink(secret, aliases); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAliases(aliases); err == nil {
		t.Error("OpenAliases followed a symlink")
	}
	a := &Aliases{path: aliases, m: map[string]map[string]string{}}
	if _, err := a.allocateShared(KindInstance, "c1"); err == nil {
		t.Error("allocateShared wrote through a symlink")
	}
	if data, _ := os.ReadFile(secret); string(data) != content {
		t.Errorf("the symlink's target changed: %s", data)
	}

	fifo := filepath.Join(dir, "0002-fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(fifo); err == nil {
		t.Error("ReadRecord read a FIFO")
	}
}

package metadata

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	if err := LoadDotEnv(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("bad line here\nSECRET=canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(bad); err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "bad line") {
		t.Errorf("a malformed file: %v", err)
	}
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("LXD_SPIFFE_TEST_DOTENV=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("LXD_SPIFFE_TEST_DOTENV") }) //nolint:errcheck // test cleanup
	if err := LoadDotEnv(good); err != nil || os.Getenv("LXD_SPIFFE_TEST_DOTENV") != "1" {
		t.Errorf("a good file: %v", err)
	}
}

// TestPrintFull: everything goes to the writer, each field once.
func TestPrintFull(t *testing.T) {
	var b strings.Builder
	PrintFull(&b)
	out := b.String()
	if !strings.Contains(out, "  - Name ") || !strings.Contains(out, "  - Variable Name (.env) ") {
		t.Errorf("output %q", out)
	}
	if n := strings.Count(out, "  - Git Time "); n != 1 {
		t.Errorf("Git Time printed %d times", n)
	}
}

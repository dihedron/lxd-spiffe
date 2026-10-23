package agentproof

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const name = "proof-0123456789abcdef"

var content = []byte(strings.Repeat("ab", 32))

// TestFRA7CreatesDirectoryAndFile checks a missing proof directory is
// created 0700 with its missing parents, and the proof file
// 0600 with the exact content; remove deletes it.
func TestFRA7CreatesDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spire", "lxd")
	remove, err := WriteFile(dir, name, content, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode() != os.ModeDir|0o700 {
			t.Errorf("%s: %v %v; want drwx------", d, fi.Mode(), err)
		}
	}
	file := filepath.Join(dir, name)
	fi, err := os.Lstat(file)
	if err != nil || fi.Mode() != 0o600 {
		t.Fatalf("proof file: %v %v; want -rw-------", fi.Mode(), err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(content) {
		t.Errorf("content %q", got)
	}
	remove()
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after remove: %v; want not exist", err)
	}
	remove() // twice is harmless
	if _, err := WriteFile(dir, "", content, os.Getuid()); err == nil {
		t.Error("empty name accepted")
	}
}

// TestFRA7ExistingDirectory checks an existing 0700 directory of the owner
// is used as it is.
func TestFRA7ExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lxd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	remove, err := WriteFile(dir, name, content, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	remove()
}

// TestFRA7Refusals checks every unsafe directory or file is refused and
// leaves no proof behind.
func TestFRA7Refusals(t *testing.T) {
	uid := os.Getuid()
	cases := map[string]struct {
		setup func(t *testing.T, dir string)
		name  string
		owner int
	}{
		"directory of another owner": {setup: mkdir(0o700), owner: uid + 1},
		"directory mode 0755":        {setup: mkdir(0o755)},
		"directory mode 0500":        {setup: mkdir(0o500)},
		"directory is sticky":        {setup: func(t *testing.T, dir string) { mkdir(0o700)(t, dir); chmod(t, dir, os.ModeSticky|0o700) }},
		"directory is a symlink": {setup: func(t *testing.T, dir string) {
			target := filepath.Join(filepath.Dir(dir), "target")
			mkdir(0o700)(t, target)
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		}},
		"directory is a file": {setup: func(t *testing.T, dir string) { touch(t, dir) }},
		"file exists":         {setup: func(t *testing.T, dir string) { mkdir(0o700)(t, dir); touch(t, filepath.Join(dir, name)) }},
		"file is a symlink": {setup: func(t *testing.T, dir string) {
			mkdir(0o700)(t, dir)
			if err := os.Symlink(filepath.Join(filepath.Dir(dir), "elsewhere"), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}},
		"name with a slash": {setup: mkdir(0o700), name: "../" + name},
		"name is dot dot":   {setup: mkdir(0o700), name: ".."},
		"name is dot":       {setup: mkdir(0o700), name: "."},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "lxd")
			c.setup(t, dir)
			remove, err := WriteFile(dir, cmp.Or(c.name, name), content, cmp.Or(c.owner, uid))
			if err == nil {
				remove()
				t.Fatal("accepted")
			}
			t.Log(err)
			if _, serr := os.Lstat(filepath.Join(filepath.Dir(dir), "elsewhere")); serr == nil {
				t.Error("wrote through the symlink")
			}
		})
	}
}

func mkdir(mode os.FileMode) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		chmod(t, dir, mode)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

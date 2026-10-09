package evidence

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAliasesInMemory(t *testing.T) {
	a := NewAliases()
	if got := a.Alias(KindMAC, "00:16:3e:aa:bb:cc"); got != "mac-1" {
		t.Errorf("first alias = %q", got)
	}
	if got := a.Alias(KindMAC, "00:16:3e:aa:bb:cc"); got != "mac-1" {
		t.Errorf("same value, alias = %q", got)
	}
	if got := a.Alias(KindMAC, "00:16:3e:aa:bb:cd"); got != "mac-2" {
		t.Errorf("second alias = %q", got)
	}
	if got := a.Alias(KindInstance, "c1"); got != "instance-1" {
		t.Errorf("instance alias = %q", got)
	}
}

func TestAliasesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	a, err := OpenAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("aliases.json created before the first alias: %v", err)
	}
	if got := a.Alias(KindInstance, "c1"); got != "instance-1" {
		t.Errorf("alias = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o; want 600", info.Mode().Perm())
	}

	// a second process of the same run sees the same aliases
	b, err := OpenAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Alias(KindInstance, "c1"); got != "instance-1" {
		t.Errorf("reopened alias = %q", got)
	}
	if got := b.Alias(KindInstance, "c2"); got != "instance-2" {
		t.Errorf("new alias = %q", got)
	}
	// and the first one sees the second's new alias
	if got := a.Alias(KindInstance, "c2"); got != "instance-2" {
		t.Errorf("alias seen by the first = %q", got)
	}
	if names := b.Values(KindInstance); len(names) != 2 {
		t.Errorf("Values = %v", names)
	}
}

func TestAliasesConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	const n = 8
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := OpenAliases(path)
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = a.Alias(KindMAC, "00:16:3e:aa:bb:cc")
		}()
	}
	wg.Wait()
	for i, alias := range got {
		if alias != "mac-1" {
			t.Errorf("writer %d alias = %q; want mac-1", i, alias)
		}
	}
}

func TestAliasesReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.json")
	a, err := OpenAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Alias(KindInstance, "c1")
	before, _ := os.ReadFile(path)

	ro, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ro.Alias(KindInstance, "c1"); got != "instance-1" {
		t.Errorf("loaded alias = %q", got)
	}
	if got := ro.Alias(KindInstance, "c9"); got != "instance-2" {
		t.Errorf("new in-memory alias = %q", got)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("a loaded alias map wrote to its file")
	}
}

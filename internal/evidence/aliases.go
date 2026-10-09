package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"sync"
	"syscall"
)

// Kinds of aliased values. The kind is also the prefix of the alias.
const (
	KindMAC      = "mac"
	KindInstance = "instance"
	KindProject  = "project"
)

// Aliases maps real values to stable aliases such as mac-1 or instance-2
// (PRE-03, PRE-09). A map opened on a file (OpenAliases) is shared by every
// process of a run: each new alias is allocated under an exclusive lock on
// the file, so all records of the run use the same alias for a value. The
// file is created with the first alias, mode 0600.
type Aliases struct {
	mu   sync.Mutex
	path string // empty: in memory only
	m    map[string]map[string]string
}

// NewAliases returns an alias map kept in memory only.
func NewAliases() *Aliases {
	return &Aliases{m: map[string]map[string]string{}}
}

// OpenAliases returns an alias map backed by the file at path; the file is
// created with the first alias.
func OpenAliases(path string) (*Aliases, error) {
	a := &Aliases{path: path, m: map[string]map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading aliases: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &a.m); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", path, err)
		}
	}
	return a, nil
}

// LoadAliases reads the alias map at path into memory; new aliases are not
// written back. A missing file gives an empty map.
func LoadAliases(path string) (*Aliases, error) {
	a, err := OpenAliases(path)
	if err != nil {
		return nil, err
	}
	a.path = ""
	return a, nil
}

// Alias returns the alias of value, allocating the next one of its kind if
// needed. When the file cannot be updated, the alias is still returned and
// kept in memory, so sanitization never fails open.
func (a *Aliases) Alias(kind, value string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if alias, ok := a.m[kind][value]; ok {
		return alias
	}
	if a.path != "" {
		if alias, err := a.allocateShared(kind, value); err == nil {
			return alias
		}
	}
	return a.allocate(kind, value)
}

// Values returns the real values of a kind, sorted.
func (a *Aliases) Values(kind string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := make([]string, 0, len(a.m[kind]))
	for v := range a.m[kind] {
		values = append(values, v)
	}
	slices.Sort(values)
	return values
}

// kinds returns the kinds present in the map.
func (a *Aliases) kinds() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	kinds := make([]string, 0, len(a.m))
	for k := range a.m {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return kinds
}

func (a *Aliases) allocate(kind, value string) string {
	if a.m[kind] == nil {
		a.m[kind] = map[string]string{}
	}
	alias := kind + "-" + strconv.Itoa(len(a.m[kind])+1)
	a.m[kind][value] = alias
	return alias
}

// allocateShared re-reads the file under an exclusive lock, so that another
// process's aliases are seen, allocates if still needed and writes it back.
func (a *Aliases) allocateShared(kind, value string) (string, error) {
	f, err := os.OpenFile(a.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // released on close anyway

	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if len(data) > 0 {
		m := map[string]map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			return "", err
		}
		a.m = m
	}
	if alias, ok := a.m[kind][value]; ok {
		return alias, nil
	}
	alias := a.allocate(kind, value)
	data, err = json.MarshalIndent(a.m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := f.Truncate(0); err != nil {
		return "", err
	}
	if _, err := f.WriteAt(append(data, '\n'), 0); err != nil {
		return "", err
	}
	return alias, nil
}

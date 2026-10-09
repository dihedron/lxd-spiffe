package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// aliasesFile is the alias map of a run directory (PRE-09).
const aliasesFile = "aliases.json"

// ErrBadOutput is returned when the output directory of SanitizeTree is
// inside the source or not empty.
var ErrBadOutput = errors.New("invalid output directory")

// TreeReport is the result of SanitizeTree.
type TreeReport struct {
	// Files is the number of files examined.
	Files int
	// Changed lists the files that sanitizing changes (or changed), relative
	// to the source directory.
	Changed []string
	// Forbidden lists what cannot be sanitized: alias maps (in a check),
	// symlinks and files that are not UTF-8.
	Forbidden []string
	// AliasMaps lists the alias maps found; in place they are kept, with a
	// copy they are left out.
	AliasMaps []string
}

// OK reports whether the tree is sanitized: nothing would change and
// nothing is forbidden.
func (r *TreeReport) OK() bool {
	return len(r.Changed) == 0 && len(r.Forbidden) == 0
}

// SanitizeTree applies the sanitization rules to every file under src
// (PRU-05). JSON files are sanitized as JSON, .jsonl files line by line and
// the others as text. A directory holding an aliases.json also uses that
// alias map, so the names it lists are replaced.
//
// With check, nothing is written and the report says what would change.
// Otherwise the files are rewritten in place, or, when out is not empty,
// copied sanitized into out, which must not exist or be empty and must not
// be inside src; aliases.json is never copied. In place, aliases.json is
// kept because the run may continue, so a later check still reports it.
func SanitizeTree(src, out string, check bool) (*TreeReport, error) {
	src = filepath.Clean(src)
	if out != "" {
		if err := checkOut(src, out); err != nil {
			return nil, err
		}
	}
	report := &TreeReport{}
	sanitizers := map[string]*Sanitizer{}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if out != "" {
				return os.MkdirAll(filepath.Join(out, rel), 0o700)
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0, !d.Type().IsRegular():
			report.Forbidden = append(report.Forbidden, rel)
			return nil
		case d.Name() == aliasesFile:
			report.AliasMaps = append(report.AliasMaps, rel)
			if check {
				report.Forbidden = append(report.Forbidden, rel)
			}
			return nil
		}
		report.Files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			report.Forbidden = append(report.Forbidden, rel)
			return nil
		}
		s, err := sanitizerFor(filepath.Dir(path), src, sanitizers, check || out != "")
		if err != nil {
			return err
		}
		clean, changed := sanitizeFile(s, path, data)
		if changed {
			report.Changed = append(report.Changed, rel)
		}
		switch {
		case check:
			return nil
		case out != "":
			return writeFile(filepath.Join(out, rel), clean)
		case changed:
			return writeFile(path, clean)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sanitizing %s: %w", src, err)
	}
	slices.Sort(report.Changed)
	slices.Sort(report.Forbidden)
	slices.Sort(report.AliasMaps)
	return report, nil
}

// checkOut refuses an output directory that is inside src or not empty.
func checkOut(src, out string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if absOut == absSrc || strings.HasPrefix(absOut, absSrc+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is inside %s", ErrBadOutput, out, src)
	}
	entries, err := os.ReadDir(out)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s is not empty", ErrBadOutput, out)
	}
	return nil
}

// sanitizerFor returns the sanitizer of a directory: the one of the nearest
// directory, up to root, that holds an aliases.json, or a sanitizer with
// in-memory aliases. readOnly loads the alias map without writing to it.
func sanitizerFor(dir, root string, cache map[string]*Sanitizer, readOnly bool) (*Sanitizer, error) {
	if s, ok := cache[dir]; ok {
		return s, nil
	}
	var s *Sanitizer
	path := filepath.Join(dir, aliasesFile)
	if _, err := os.Lstat(path); err == nil {
		open := OpenAliases
		if readOnly {
			open = LoadAliases
		}
		aliases, err := open(path)
		if err != nil {
			return nil, err
		}
		s = NewSanitizer(aliases)
		for _, kind := range aliases.kinds() {
			if kind == KindMAC {
				continue
			}
			for _, name := range aliases.Values(kind) {
				s.RedactName(kind, name)
			}
		}
	} else if dir == root || filepath.Dir(dir) == dir {
		s = NewSanitizer(NewAliases())
	} else {
		parent, err := sanitizerFor(filepath.Dir(dir), root, cache, readOnly)
		if err != nil {
			return nil, err
		}
		s = parent
	}
	cache[dir] = s
	return s, nil
}

// sanitizeFile returns the sanitized content of a file and whether it
// differs from the original. JSON is compared as values, so formatting
// alone is not a change.
func sanitizeFile(s *Sanitizer, path string, data []byte) ([]byte, bool) {
	switch filepath.Ext(path) {
	case ".json":
		if v, err := decodeJSON(data); err == nil {
			clean := s.Value(v)
			if reflect.DeepEqual(v, clean) {
				return data, false
			}
			if out, err := json.MarshalIndent(clean, "", "  "); err == nil {
				return append(out, '\n'), true
			}
		}
	case ".jsonl":
		var b bytes.Buffer
		changed := false
		for _, line := range strings.SplitAfter(string(data), "\n") {
			trimmed := strings.TrimSuffix(line, "\n")
			if v, err := decodeJSON([]byte(trimmed)); err == nil && trimmed != "" {
				if clean := s.Value(v); !reflect.DeepEqual(v, clean) {
					if out, err := json.Marshal(clean); err == nil {
						changed = true
						b.Write(out)
						b.WriteString(line[len(trimmed):])
						continue
					}
				}
				b.WriteString(line)
				continue
			}
			clean := s.String(line)
			changed = changed || clean != line
			b.WriteString(clean)
		}
		return b.Bytes(), changed
	}
	clean := s.String(string(data))
	return []byte(clean), clean != string(data)
}

// writeFile replaces path with data, mode 0600, through a temporary file.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sanitize-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Package safety implements the write rules of lxd-probe (spec: Trust model
// → Writes): a write needs explicit acknowledgement (PRN-01) and may only
// target the lab config keys and guest paths (PRN-02). Every check runs
// before any request is sent or any file is touched.
package safety

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

const (
	// LabKeyPrefix is the prefix of the config keys the probe may write.
	LabKeyPrefix = "user.lxd-probe."
	// SpireKeyPrefix is the prefix of the attestor's challenge keys, writable
	// with --allow-spire-keys only.
	SpireKeyPrefix = "user.spire.challenge."
	// LabDir is the guest directory the probe may write under.
	LabDir = "/run/lxd-probe"
	// SpireDir is the attestor's proof directory, writable with
	// --allow-spire-paths only.
	SpireDir = "/run/spire/lxd"
)

// Write refuses a write verb unless both --allow-write and --lab are given
// (PRN-01).
func Write(allowWrite, lab bool) error {
	var missing []string
	if !allowWrite {
		missing = append(missing, "--allow-write")
	}
	if !lab {
		missing = append(missing, "--lab")
	}
	if len(missing) > 0 {
		return exit.New(exit.Refused, fmt.Errorf("write refused: %s required", strings.Join(missing, " and ")))
	}
	return nil
}

// Key refuses a config key outside user.lxd-probe.*, or outside
// user.spire.challenge.* when allowSpire is set (PRN-02). The part after the
// prefix must be non-empty and use only letters, digits, '.', '_' and '-'.
func Key(key string, allowSpire bool) error {
	prefixes := []string{LabKeyPrefix}
	if allowSpire {
		prefixes = append(prefixes, SpireKeyPrefix)
	}
	for _, prefix := range prefixes {
		if name, ok := strings.CutPrefix(key, prefix); ok {
			if err := keyName(name); err != nil {
				return exit.New(exit.Refused, fmt.Errorf("write refused: key %q: %w", key, err))
			}
			return nil
		}
	}
	return exit.New(exit.Refused, fmt.Errorf("write refused: key %q is outside %s", key, strings.Join(withStar(prefixes), " and ")))
}

func keyName(name string) error {
	if name == "" {
		return errors.New("empty name after the prefix")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("character %q not allowed", r)
		}
	}
	return nil
}

// Path refuses a guest path that is not strictly below /run/lxd-probe, or
// below /run/spire/lxd when allowSpire is set (PRN-02). The path must be
// absolute and already clean: "..", "." and repeated or trailing slashes are
// refused rather than normalised. The check is lexical; the guest verbs also
// open files without following symlinks.
func Path(p string, allowSpire bool) error {
	dirs := []string{LabDir}
	if allowSpire {
		dirs = append(dirs, SpireDir)
	}
	if !path.IsAbs(p) || path.Clean(p) != p {
		return exit.New(exit.Refused, fmt.Errorf("write refused: path %q is not an absolute clean path", p))
	}
	for _, dir := range dirs {
		if strings.HasPrefix(p, dir+"/") {
			return nil
		}
	}
	return exit.New(exit.Refused, fmt.Errorf("write refused: path %q is outside %s", p, strings.Join(withSlash(dirs), " and ")))
}

func withStar(prefixes []string) []string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = p + "*"
	}
	return out
}

func withSlash(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = d + "/"
	}
	return out
}

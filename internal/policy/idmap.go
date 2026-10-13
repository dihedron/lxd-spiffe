// Package policy holds the attestor's decision rules that lxd-probe also
// runs, so that both use the same code (attestor spec Proof of co-location
// → UID translation and the isolation check; probe spec PRU-02, PRU-04).
package policy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// IDMapEntry is one entry of an LXD idmap (volatile.idmap.current), as LXD
// writes it; /proc/<pid>/uid_map lines read into the same form.
type IDMapEntry struct {
	Isuid    bool
	Isgid    bool
	Hostid   int64
	Nsid     int64
	Maprange int64
}

// Reason is why a translation is refused.
type Reason string

// Refusal reasons: the translation fails closed (attestor spec, UID
// translation).
const (
	Missing     Reason = "missing"      // no map
	Unparseable Reason = "unparseable"  // not an idmap
	OutOfRange  Reason = "out-of-range" // a value outside 32 bits, or an empty range
	Overlap     Reason = "overlap"      // uid entries whose namespace or host ranges overlap
	NoEntry     Reason = "no-entry"     // no entry maps the uid
)

// Refusal is a refused translation.
type Refusal struct {
	Reason Reason
	Detail string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("uid translation refused (%s): %s", r.Reason, r.Detail)
}

func refuse(reason Reason, format string, args ...any) error {
	return &Refusal{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// ParseIDMap parses volatile.idmap.current: a JSON list of entries.
func ParseIDMap(raw string) ([]IDMapEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, refuse(Missing, "no idmap")
	}
	var entries []IDMapEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, refuse(Unparseable, "%v", err)
	}
	return entries, nil
}

// ParseProcIDMap parses /proc/<pid>/uid_map: lines of "inside outside
// count", read as uid entries (Nsid, Hostid, Maprange).
func ParseProcIDMap(text string) ([]IDMapEntry, error) {
	entries := []IDMapEntry{}
	for line := range strings.Lines(text) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("uid_map line %q: want 3 fields", strings.TrimSpace(line))
		}
		var n [3]int64
		for i := range f {
			v, err := strconv.ParseInt(f[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("uid_map line %q: %w", strings.TrimSpace(line), err)
			}
			n[i] = v
		}
		entries = append(entries, IDMapEntry{Isuid: true, Nsid: n[0], Hostid: n[1], Maprange: n[2]})
	}
	return entries, nil
}

// uidEntries returns the uid entries, checked: every value fits in 32 bits
// and ranges are not empty, and no two namespace ranges overlap.
func uidEntries(entries []IDMapEntry) ([]IDMapEntry, error) {
	const limit = int64(1) << 32
	var uids []IDMapEntry
	for _, e := range entries {
		if !e.Isuid {
			continue
		}
		if e.Maprange <= 0 || e.Nsid < 0 || e.Hostid < 0 || e.Nsid+e.Maprange > limit || e.Hostid+e.Maprange > limit {
			return nil, refuse(OutOfRange, "entry %+v", e)
		}
		uids = append(uids, e)
	}
	// overlapping namespace ranges make the translation ambiguous, and
	// overlapping host ranges map two uids onto one: both are refused
	for _, side := range []func(IDMapEntry) int64{
		func(e IDMapEntry) int64 { return e.Nsid },
		func(e IDMapEntry) int64 { return e.Hostid },
	} {
		sorted := slices.Clone(uids)
		slices.SortFunc(sorted, func(a, b IDMapEntry) int { return cmp.Compare(side(a), side(b)) })
		for i := 1; i < len(sorted); i++ {
			if side(sorted[i]) < side(sorted[i-1])+sorted[i-1].Maprange {
				return nil, refuse(Overlap, "entries %+v and %+v", sorted[i-1], sorted[i])
			}
		}
	}
	return uids, nil
}

// TranslateUID returns the host uid of namespace uid through the uid
// entries of an idmap: exactly one entry must cover it, and the result is
// Hostid + (uid - Nsid). Anything else is refused (fail closed).
func TranslateUID(entries []IDMapEntry, uid uint32) (uint32, error) {
	uids, err := uidEntries(entries)
	if err != nil {
		return 0, err
	}
	var match []IDMapEntry
	for _, e := range uids {
		if int64(uid) >= e.Nsid && int64(uid) < e.Nsid+e.Maprange {
			match = append(match, e)
		}
	}
	if len(match) != 1 {
		return 0, refuse(NoEntry, "%d entries map uid %d", len(match), uid)
	}
	return uint32(match[0].Hostid + int64(uid) - match[0].Nsid), nil
}

// CompareUIDMaps reports whether an LXD idmap's uid entries are the ranges
// of a /proc uid_map, and describes the first difference.
func CompareUIDMaps(lxd, proc []IDMapEntry) (bool, string) {
	norm := func(entries []IDMapEntry) []IDMapEntry {
		var out []IDMapEntry
		for _, e := range entries {
			if e.Isuid {
				out = append(out, IDMapEntry{Isuid: true, Nsid: e.Nsid, Hostid: e.Hostid, Maprange: e.Maprange})
			}
		}
		slices.SortFunc(out, func(a, b IDMapEntry) int { return int(a.Nsid - b.Nsid) })
		return out
	}
	a, b := norm(lxd), norm(proc)
	if len(a) != len(b) {
		return false, fmt.Sprintf("%d uid entries in LXD's map, %d in the guest's", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return false, fmt.Sprintf("LXD maps %d..%d to %d, the guest %d..%d to %d",
				a[i].Nsid, a[i].Nsid+a[i].Maprange-1, a[i].Hostid, b[i].Nsid, b[i].Nsid+b[i].Maprange-1, b[i].Hostid)
		}
	}
	return true, ""
}

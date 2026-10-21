package stress

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// Evacuation reliability (ST-6, STR-70–STR-76): cycles of evacuate and
// restore of one member, measured per LXD version, arm and instance type.

// The arms of ST-6 (STR-72).
const (
	ArmIdle  = "idle"  // no traffic during the cycle
	ArmFiles = "files" // file-API reads during evacuate and restore
)

// The instance types, as LXD names them.
const (
	Container = "container"
	VM        = "virtual-machine"
)

// EvacArms is the order of the cycles: the arms alternate, idle first,
// cycles times each, so that drift over time hits both alike.
func EvacArms(cycles int) []string {
	out := make([]string, 0, 2*cycles)
	for range cycles {
		out = append(out, ArmIdle, ArmFiles)
	}
	return out
}

// Placement is where an instance is after a step, and in which state.
type Placement struct {
	Member string `json:"member"`
	State  string `json:"state"`
	Type   string `json:"type"`
}

// EvacStep is an evacuate or a restore of a cycle (STR-71).
type EvacStep struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Tries      int    `json:"tries"`
	DurationMS int64  `json:"duration_ms"`
	// Placement is where each instance ended.
	Placement map[string]Placement `json:"placement"`
	// Orphans are the instance volumes without their instance after the
	// step, as name@member.
	Orphans []string `json:"orphans,omitempty"`
	// Busy are the datasets LXD failed to delete as busy, and Holders the
	// processes whose mount namespace still held each (STR-73), as "pid
	// command line".
	Busy    []string            `json:"busy,omitempty"`
	Holders map[string][]string `json:"holders,omitempty"`
	// Errors are LXD's errors logged on the members during the step.
	Errors []string `json:"errors,omitempty"`
	// Reads and ReadErrors count the file-API reads of the files arm
	// during the step.
	Reads      int `json:"reads,omitempty"`
	ReadErrors int `json:"read_errors,omitempty"`
}

// EvacCycle is one cycle's record, a line of results.jsonl (STR-75).
type EvacCycle struct {
	N       int    `json:"n"`
	Server  string `json:"server"`
	Version string `json:"version"`
	Arm     string `json:"arm"`
	Evacuee string `json:"evacuee"`
	// Types are the evacuation fleet's instances and their types.
	Types    map[string]string `json:"types"`
	Start    time.Time         `json:"start"`
	End      time.Time         `json:"end"`
	Evacuate EvacStep          `json:"evacuate"`
	Restore  EvacStep          `json:"restore"`
	// Blocked: the restore could not succeed (STR-74); Reset: the cluster
	// was reset and the fleet rebuilt after it.
	Blocked bool `json:"blocked,omitempty"`
	Reset   bool `json:"reset,omitempty"`
	// Notes say what the harness did to put the fleet back after the
	// cycle: instances started or moved back.
	Notes []string `json:"notes,omitempty"`
	// HarnessError: the cycle could not run (STR-76); it counts no
	// migration.
	HarnessError string `json:"harness_error,omitempty"`
}

// EvacGroup counts the migrations of one LXD version, arm and instance
// type.
type EvacGroup struct {
	Version           string `json:"version"`
	Arm               string `json:"arm"`
	Type              string `json:"type"`
	EvacuateAttempted int    `json:"evacuate_attempted"`
	EvacuateFailed    int    `json:"evacuate_failed"`
	RestoreAttempted  int    `json:"restore_attempted"`
	RestoreFailed     int    `json:"restore_failed"`
	VolumesLeft       int    `json:"volumes_left"`
	BusyErrors        int    `json:"busy_errors"`
}

// Rate is the failure rate of the group's migrations, both directions: the
// observed rate when some failed, otherwise the rule of three's upper
// bound (95 %) on it (STR-76).
func (g EvacGroup) Rate() string {
	n := g.EvacuateAttempted + g.RestoreAttempted
	failed := g.EvacuateFailed + g.RestoreFailed
	switch {
	case n == 0:
		return "n/a"
	case failed == 0:
		return fmt.Sprintf("below %.1f %% (none failed of %d)", 300/float64(n), n)
	}
	return fmt.Sprintf("%.1f %% (%d failed of %d)", 100*float64(failed)/float64(n), failed, n)
}

// EvacCycles sums the cycles of one LXD version and arm.
type EvacCycles struct {
	Version        string  `json:"version"`
	Arm            string  `json:"arm"`
	Cycles         int     `json:"cycles"`
	Blocked        int     `json:"blocked"`
	EvacuateErrors int     `json:"evacuate_errors"`
	RestoreErrors  int     `json:"restore_errors"`
	Evacuate       Latency `json:"evacuate"`
	Restore        Latency `json:"restore"`
}

// EvacSummary is the result of an ST-6 run (STR-75).
type EvacSummary struct {
	Settings      map[string]string `json:"settings"`
	Groups        []EvacGroup       `json:"groups"`
	Cycles        []EvacCycles      `json:"cycles"`
	Holders       []string          `json:"holders"`
	HarnessErrors []string          `json:"harness_errors"`
}

// OK reports whether the harness ran every cycle: ST-6 has no invariant
// (STR-76).
func (s EvacSummary) OK() bool { return len(s.HarnessErrors) == 0 }

// SummarizeEvac computes the summary of cycles.
func SummarizeEvac(cycles []EvacCycle, settings map[string]string) EvacSummary {
	s := EvacSummary{Settings: settings, Groups: []EvacGroup{}, Cycles: []EvacCycles{}, Holders: []string{}, HarnessErrors: []string{}}
	type gkey struct{ version, arm, typ string }
	type ckey struct{ version, arm string }
	groups := map[gkey]*EvacGroup{}
	sums := map[ckey]*EvacCycles{}
	durations := map[ckey][2][]int64{}
	for _, c := range cycles {
		if c.HarnessError != "" {
			s.HarnessErrors = append(s.HarnessErrors, fmt.Sprintf("cycle %d (%s, %s): %s", c.N, c.Server, c.Arm, c.HarnessError))
			continue
		}
		group := func(typ string) *EvacGroup {
			k := gkey{c.Version, c.Arm, typ}
			if groups[k] == nil {
				groups[k] = &EvacGroup{Version: c.Version, Arm: c.Arm, Type: typ}
			}
			return groups[k]
		}
		for name, typ := range c.Types {
			g := group(typ)
			g.EvacuateAttempted++
			if !placed(c.Evacuate.Placement[name], c.Evacuee, false) {
				g.EvacuateFailed++
				continue
			}
			// only an instance the evacuation moved has a restore to make
			g.RestoreAttempted++
			if !placed(c.Restore.Placement[name], c.Evacuee, true) {
				g.RestoreFailed++
			}
		}
		left := map[string]bool{}
		for _, step := range []EvacStep{c.Evacuate, c.Restore} {
			for _, o := range step.Orphans {
				name, _, _ := strings.Cut(o, "@")
				if !left[o] {
					left[o] = true
					group(cmp.Or(c.Types[name], "unknown")).VolumesLeft++
				}
			}
			for _, ds := range step.Busy {
				group(cmp.Or(c.Types[DatasetInstance(ds)], "unknown")).BusyErrors++
				holders := "no holder found"
				if h := step.Holders[ds]; len(h) > 0 {
					holders = strings.Join(h, "; ")
				}
				s.Holders = append(s.Holders, fmt.Sprintf("cycle %d, %s, %s: %s", c.N, c.Evacuee, ds, holders))
			}
		}
		k := ckey{c.Version, c.Arm}
		if sums[k] == nil {
			sums[k] = &EvacCycles{Version: c.Version, Arm: c.Arm}
		}
		sum := sums[k]
		sum.Cycles++
		if c.Blocked {
			sum.Blocked++
		}
		if !c.Evacuate.OK {
			sum.EvacuateErrors++
		}
		if !c.Restore.OK {
			sum.RestoreErrors++
		}
		d := durations[k]
		durations[k] = [2][]int64{append(d[0], c.Evacuate.DurationMS), append(d[1], c.Restore.DurationMS)}
	}
	for _, k := range slices.SortedFunc(maps.Keys(groups), func(a, b gkey) int {
		return cmp.Or(cmp.Compare(a.version, b.version), cmp.Compare(a.arm, b.arm), cmp.Compare(a.typ, b.typ))
	}) {
		s.Groups = append(s.Groups, *groups[k])
	}
	for _, k := range slices.SortedFunc(maps.Keys(sums), func(a, b ckey) int {
		return cmp.Or(cmp.Compare(a.version, b.version), cmp.Compare(a.arm, b.arm))
	}) {
		sum := sums[k]
		sum.Evacuate, sum.Restore = percentiles(durations[k][0]), percentiles(durations[k][1])
		s.Cycles = append(s.Cycles, *sum)
	}
	return s
}

// placed reports whether an instance ended where its step should leave
// it, running: on the evacuee after a restore, elsewhere after an
// evacuation.
func placed(p Placement, evacuee string, onEvacuee bool) bool {
	return p.Member != "" && (p.Member == evacuee) == onEvacuee && p.State == "RUNNING"
}

// DatasetInstance is the instance a ZFS dataset of LXD belongs to:
// pool/containers/[project_]name, pool/virtual-machines/[project_]name
// with a ".block" for a VM's disk, and "@snapshot" for a snapshot.
// Instance names have no "_", so a project prefix ends at the last one.
func DatasetInstance(dataset string) string {
	name := dataset[strings.LastIndex(dataset, "/")+1:]
	name, _, _ = strings.Cut(name, "@")
	name = strings.TrimSuffix(name, ".block")
	return name[strings.LastIndex(name, "_")+1:]
}

// WriteJSON writes the summary as summary.json's content.
func (s EvacSummary) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// WriteMarkdown writes the summary as summary.md's content.
func (s EvacSummary) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# Evacuation reliability summary\n\n")
	verdict := "**No harness error.**"
	if !s.OK() {
		verdict = fmt.Sprintf("**%d harness errors.**", len(s.HarnessErrors))
	}
	cycles, blocked := 0, 0
	for _, c := range s.Cycles {
		cycles, blocked = cycles+c.Cycles, blocked+c.Blocked
	}
	fmt.Fprintf(&b, "%s %d cycles, %d blocked.\n\n## Settings\n\n", verdict, cycles, blocked)
	for _, k := range slices.Sorted(maps.Keys(s.Settings)) {
		fmt.Fprintf(&b, "- `%s` = `%s`\n", k, s.Settings[k])
	}
	b.WriteString("\n## Migrations\n\n| LXD | Arm | Type | Evacuate failed | Restore failed | Volumes left | Busy datasets | Failure rate |\n|---|---|---|---|---|---|---|---|\n")
	for _, g := range s.Groups {
		fmt.Fprintf(&b, "| %s | %s | %s | %d of %d | %d of %d | %d | %d | %s |\n", g.Version, g.Arm, g.Type,
			g.EvacuateFailed, g.EvacuateAttempted, g.RestoreFailed, g.RestoreAttempted, g.VolumesLeft, g.BusyErrors, g.Rate())
	}
	b.WriteString("\n## Cycles\n\n| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range s.Cycles {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d ms | %d ms | %d ms | %d ms |\n", c.Version, c.Arm, c.Cycles, c.Blocked,
			c.EvacuateErrors, c.RestoreErrors, c.Evacuate.P50, c.Evacuate.Max, c.Restore.P50, c.Restore.Max)
	}
	b.WriteString("\n## Busy datasets and their holders (STR-73)\n\n")
	if len(s.Holders) == 0 {
		b.WriteString("None.\n")
	}
	for _, h := range s.Holders {
		fmt.Fprintf(&b, "- %s\n", h)
	}
	b.WriteString("\n## Harness errors\n\n")
	if len(s.HarnessErrors) == 0 {
		b.WriteString("None.\n")
	}
	for _, e := range s.HarnessErrors {
		fmt.Fprintf(&b, "- %s\n", e)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteCycles writes one JSON line per cycle (results.jsonl).
func WriteCycles(w io.Writer, cycles []EvacCycle) error {
	enc := json.NewEncoder(w)
	for _, c := range cycles {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return nil
}

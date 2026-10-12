package util

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// Diff is util diff A B (PRU-01): two records compared observation by
// observation, ignoring what changes on every run.
type Diff struct {
	probe.GlobalOptions
	IgnoreETag bool `long:"ignore-etag" description:"Ignore ETag values."`
	Args       struct {
		A string `positional-arg-name:"A" description:"First record."`
		B string `positional-arg-name:"B" description:"Second record."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// difference is one difference between the records.
type difference struct {
	Path string `json:"path"`
	A    any    `json:"a"`
	B    any    `json:"b"`
}

// volatileFields change on every run: timestamps and run-specific ids.
var volatileFields = []string{"mtime", "time", "created_at", "last_used_at", "x-lxd-modified"}

// Execute runs the command.
func (cmd *Diff) Execute(args []string) error {
	a, err := readRecord(cmd.Args.A)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("util diff: %w", err))
	}
	b, err := readRecord(cmd.Args.B)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("util diff: %w", err))
	}
	recorded := []string{filepath.Base(cmd.Args.A), filepath.Base(cmd.Args.B)}
	if cmd.IgnoreETag {
		recorded = append(recorded, "--ignore-etag")
	}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "diff", recorded)
	if err != nil {
		return err
	}
	s.Observe("diff.records", map[string]string{"a": a.ID, "b": b.ID})
	diffs := cmd.compare(a, b)
	s.Observe("diff.count", len(diffs))
	s.Observe("diff.differences", diffs)
	return s.Finish(nil)
}

func (cmd *Diff) compare(a, b *evidence.Record) []difference {
	var out []difference
	add := func(path string, x, y any) {
		cmd.walk(path, generic(x), generic(y), &out)
	}
	add("verb", a.Verb, b.Verb)
	add("args", a.Args, b.Args)
	add("outcome", a.Outcome, b.Outcome)
	add("expectations", a.Expectations, b.Expectations)
	add("server", a.Server, b.Server)
	obsA, obsB := observations(a), observations(b)
	keys := slices.Sorted(func(yield func(string) bool) {
		seen := map[string]bool{}
		for _, m := range []map[string]any{obsA, obsB} {
			for k := range m {
				if !seen[k] {
					seen[k] = true
					if !yield(k) {
						return
					}
				}
			}
		}
	})
	for _, k := range keys {
		if cmd.ignored(k) {
			continue
		}
		cmd.walk("observations."+k, generic(obsA[k]), generic(obsB[k]), &out)
	}
	return out
}

// observations returns a record's observations by key; a repeated key
// gets #2, #3... after the first.
func observations(rec *evidence.Record) map[string]any {
	out := map[string]any{}
	count := map[string]int{}
	for _, o := range rec.Observations {
		count[o.Key]++
		key := o.Key
		if count[o.Key] > 1 {
			key = fmt.Sprintf("%s#%d", o.Key, count[o.Key])
		}
		out[key] = o.Value
	}
	return out
}

// ignored reports whether a key or field changes on every run.
func (cmd *Diff) ignored(name string) bool {
	base := strings.ToLower(name[strings.LastIndex(name, ".")+1:])
	switch {
	case strings.HasSuffix(base, "_ms"), base == "operation", slices.Contains(volatileFields, base):
		return true
	case cmd.IgnoreETag && strings.Contains(base, "etag"):
		return true
	}
	return false
}

// walk records the differences between two JSON values under path.
func (cmd *Diff) walk(path string, x, y any, out *[]difference) {
	mx, okx := x.(map[string]any)
	my, oky := y.(map[string]any)
	if okx && oky {
		keys := map[string]bool{}
		for k := range mx {
			keys[k] = true
		}
		for k := range my {
			keys[k] = true
		}
		for _, k := range slices.Sorted(func(yield func(string) bool) {
			for k := range keys {
				if !yield(k) {
					return
				}
			}
		}) {
			if cmd.ignored(k) {
				continue
			}
			cmd.walk(path+"."+k, mx[k], my[k], out)
		}
		return
	}
	lx, okx := x.([]any)
	ly, oky := y.([]any)
	if okx && oky && len(lx) == len(ly) {
		for i := range lx {
			cmd.walk(fmt.Sprintf("%s[%d]", path, i), lx[i], ly[i], out)
		}
		return
	}
	if !jsonEqual(x, y) {
		*out = append(*out, difference{Path: path, A: x, B: y})
	}
}

// generic turns a value into its generic JSON form.
func generic(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var g any
	if json.Unmarshal(data, &g) != nil {
		return v
	}
	return g
}

func jsonEqual(x, y any) bool {
	a, _ := json.Marshal(x)
	b, _ := json.Marshal(y)
	return string(a) == string(b)
}

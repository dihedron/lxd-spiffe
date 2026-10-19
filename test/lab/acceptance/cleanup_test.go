package acceptance

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stoppedInstances reads "lxc list -c ns -f csv" and returns the fleet's
// instances that are stopped or in error, but those skip names.
func stoppedInstances(list string, fleet []string, skip func(string) bool) []string {
	var out []string
	for ln := range strings.SplitSeq(strings.TrimSpace(list), "\n") {
		name, status, _ := strings.Cut(strings.TrimSpace(ln), ",")
		if !slices.Contains(fleet, name) || skip(name) {
			continue
		}
		if status == "STOPPED" || status == "ERROR" {
			out = append(out, name)
		}
	}
	return out
}

// TestStoppedInstances: only the fleet's stopped or failed instances, and
// not those skipped (the fault's own target, or one another fault acts on).
func TestStoppedInstances(t *testing.T) {
	list := "c-unpriv,RUNNING\ns-001,STOPPED\ns-002,ERROR\ns-003,STOPPED\ns-004,RUNNING\nother,STOPPED\n"
	fleet := []string{"c-unpriv", "s-001", "s-002", "s-003", "s-004"}
	got := stoppedInstances(list, fleet, func(n string) bool { return n == "s-003" })
	if want := []string{"s-001", "s-002"}; !slices.Equal(got, want) {
		t.Errorf("stopped %v, want %v", got, want)
	}
	if got := stoppedInstances("", fleet, func(string) bool { return false }); len(got) != 0 {
		t.Errorf("empty list: %v", got)
	}
}

// orphanVolumes reads "lxc storage volume list POOL -f csv" (type, name,
// description, content type, used by, location) and "lxc list -c nL -f
// csv" (name, location), and returns the instance volumes whose instance
// is not on their member, as name@member: what a failed evacuation leaves
// behind. On a standalone server the location is empty.
func orphanVolumes(volumes, instances string) []string {
	have := map[string]bool{}
	for ln := range strings.SplitSeq(strings.TrimSpace(instances), "\n") {
		if f := strings.Split(strings.TrimSpace(ln), ","); f[0] != "" {
			have[f[0]+"@"+field(f, 1)] = true
		}
	}
	var out []string
	for ln := range strings.SplitSeq(strings.TrimSpace(volumes), "\n") {
		f := strings.Split(strings.TrimSpace(ln), ",")
		if f[0] != "container" && f[0] != "virtual-machine" {
			continue
		}
		if key := field(f, 1) + "@" + field(f, 5); !have[key] {
			out = append(out, key)
		}
	}
	return out
}

// field is f[i], or "" past its end.
func field(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// TestOrphanVolumes: a volume on a member that no longer holds its
// instance is an orphan; images, custom volumes and the volumes of
// instances in place are not.
func TestOrphanVolumes(t *testing.T) {
	vols := "container,c-unpriv,,filesystem,1,c5n1\ncontainer,s-002,,filesystem,1,c5n2\ncontainer,s-005,,filesystem,1,c5n2\n" +
		"virtual-machine,v-ubuntu,,block,1,c5n2\nimage,7a3e,,filesystem,1,c5n1\ncustom,data,,filesystem,0,c5n3\n"
	insts := "c-unpriv,c5n1\nv-ubuntu,c5n2\ns-005,c5n3\n"
	if got, want := orphanVolumes(vols, insts), []string{"s-002@c5n2", "s-005@c5n2"}; !slices.Equal(got, want) {
		t.Errorf("orphans %v, want %v", got, want)
	}
	// standalone: no location column
	if got := orphanVolumes("container,c-unpriv,,filesystem,1\ncontainer,s-001,,filesystem,1\n", "c-unpriv\n"); !slices.Equal(got, []string{"s-001@"}) {
		t.Errorf("standalone orphans %v", got)
	}
	if got := orphanVolumes("", ""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

// parseLeftover reads a leftover check: with present, the guest's
// "present" or "absent" as the output's last word; without, a config
// key's value, empty when the key is gone. known is false when the check
// could not reach the instance.
func parseLeftover(out string, err error, present bool) (n int, known bool) {
	f := strings.Fields(out)
	last := ""
	if len(f) > 0 {
		last = f[len(f)-1]
	}
	switch {
	case present && last == "present":
		return 1, true
	case present && last == "absent":
		return 0, true
	case present || err != nil:
		return 0, false
	case strings.TrimSpace(out) != "":
		return 1, true
	}
	return 0, true
}

// parseSweep reads the sweep's output: a "left INSTANCE ITEM" line per
// key or file removed, then their count.
func parseSweep(out string) (n int, left []string, err error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, ln := range lines[:len(lines)-1] {
		if f := strings.Fields(ln); len(f) == 3 && f[0] == "left" {
			left = append(left, f[1]+":"+f[2])
		}
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if n, err = strconv.Atoi(last); err != nil {
		return 0, left, fmt.Errorf("no count in %q", last)
	}
	if n != len(left) {
		return n, left, fmt.Errorf("count %d, but %d items listed", n, len(left))
	}
	return n, left, nil
}

// TestParseLeftover: the guest's answer decides; an unreachable instance
// is unknown, never "none".
func TestParseLeftover(t *testing.T) {
	failed := errors.New("exit status 1")
	for _, tc := range []struct {
		out     string
		err     error
		present bool
		n       int
		known   bool
	}{
		{"present\n", nil, true, 1, true},
		{"absent\r\n", nil, true, 0, true},
		{"Error: Instance is not running\n", failed, true, 0, false},
		{"", failed, true, 0, false},
		{"", nil, true, 0, false},
		{"4f2a…\n", nil, false, 1, true},
		{"\n", nil, false, 0, true},
		{"Error: Failed to connect\n", failed, false, 0, false},
	} {
		if n, known := parseLeftover(tc.out, tc.err, tc.present); n != tc.n || known != tc.known {
			t.Errorf("parseLeftover(%q, %v, %t) = %d, %t; want %d, %t", tc.out, tc.err, tc.present, n, known, tc.n, tc.known)
		}
	}
}

// TestParseSweep: the items listed, their count, and the mismatches.
func TestParseSweep(t *testing.T) {
	n, left, err := parseSweep("left s-002 /run/spire/lxd/proof-ab\nleft s-007 user.spire.challenge.cd\n2\n")
	if err != nil || n != 2 || !slices.Equal(left, []string{"s-002:/run/spire/lxd/proof-ab", "s-007:user.spire.challenge.cd"}) {
		t.Errorf("parseSweep = %d, %v, %v", n, left, err)
	}
	if n, left, err := parseSweep("0\n"); err != nil || n != 0 || len(left) != 0 {
		t.Errorf("empty sweep = %d, %v, %v", n, left, err)
	}
	if _, _, err := parseSweep("left s-001 x\n2\n"); err == nil {
		t.Error("a count that does not match the items passed")
	}
	if _, _, err := parseSweep("garbage"); err == nil {
		t.Error("no count passed")
	}
}

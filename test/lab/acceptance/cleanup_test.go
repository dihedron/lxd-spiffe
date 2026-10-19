package acceptance

import (
	"slices"
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

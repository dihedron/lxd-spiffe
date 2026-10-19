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

package acceptance

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/test/lab/stress"
)

// parsePlacement reads "lxc list -c nLst -f csv" (name, location, state,
// type) into where each instance is.
func parsePlacement(list string) map[string]stress.Placement {
	out := map[string]stress.Placement{}
	for ln := range strings.SplitSeq(strings.TrimSpace(list), "\n") {
		f := strings.Split(strings.TrimSpace(ln), ",")
		if f[0] == "" {
			continue
		}
		out[f[0]] = stress.Placement{Member: field(f, 1), State: field(f, 2), Type: strings.ToLower(field(f, 3))}
	}
	return out
}

// settled reports whether every one of names is running on the evacuee
// (onEvacuee) or running elsewhere: what a restore or an evacuation should
// leave.
func settled(p map[string]stress.Placement, names []string, evacuee string, onEvacuee bool) bool {
	for _, n := range names {
		pl, ok := p[n]
		if !ok || pl.State != "RUNNING" || (pl.Member == evacuee) != onEvacuee {
			return false
		}
	}
	return true
}

// busyDataset is ZFS's refusal to destroy a dataset still mounted
// somewhere, as LXD reports it.
var busyDataset = regexp.MustCompile(`cannot destroy '([^']+)': dataset is busy`)

// busyDatasets are the datasets out names as busy, once each, in order.
func busyDatasets(out string) []string {
	var ds []string
	for _, m := range busyDataset.FindAllStringSubmatch(out, -1) {
		if !slices.Contains(ds, m[1]) {
			ds = append(ds, m[1])
		}
	}
	return ds
}

// holdersScript lists, on a member, the processes whose mount namespace
// still mounts dataset (STR-73): a "holder PID COMMAND LINE" line each.
func holdersScript(dataset string) string {
	return `for f in /proc/[0-9]*/mountinfo; do ` +
		`if grep -qF -- ' - zfs ` + dataset + ` ' "$f" 2>/dev/null; then p=${f#/proc/}; p=${p%/mountinfo}; ` +
		`echo "holder $p $(tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null)"; fi; done; true`
}

// parseHolders reads holdersScript's output as "PID COMMAND LINE" items.
func parseHolders(out string) []string {
	var hs []string
	for ln := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "holder "); ok {
			hs = append(hs, strings.TrimSpace(rest))
		}
	}
	return hs
}

// lxdErrors keeps the error lines of LXD's journal output, each at most
// 300 bytes, at most 20 of them: a failed migration logs a few.
func lxdErrors(journal string) []string {
	var out []string
	for ln := range strings.SplitSeq(journal, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.Contains(ln, "level=error") && !strings.Contains(ln, "dataset is busy") {
			continue
		}
		if len(ln) > 300 {
			ln = ln[:300]
		}
		if out = append(out, ln); len(out) == 20 {
			break
		}
	}
	return out
}

// parseReads sums the files arm's counts: a "reads FILE OK FAILED" line
// per reader.
func parseReads(out string) (ok, failed int) {
	for ln := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(ln)
		if len(f) != 4 || f[0] != "reads" {
			continue
		}
		o, err1 := strconv.Atoi(f[2])
		n, err2 := strconv.Atoi(f[3])
		if err1 == nil && err2 == nil {
			ok, failed = ok+o, failed+n
		}
	}
	return ok, failed
}

// TestParseReads: the readers' counts summed, other lines ignored.
func TestParseReads(t *testing.T) {
	if ok, failed := parseReads("reads /tmp/evac-reads-e-c1 120 2\nreads /tmp/evac-reads-e-v1 80 0\npkill: x\n"); ok != 200 || failed != 2 {
		t.Errorf("reads %d, %d", ok, failed)
	}
	if ok, failed := parseReads(""); ok+failed != 0 {
		t.Errorf("no reader: %d, %d", ok, failed)
	}
}

// TestParsePlacement: the columns as lxc prints them, the type in LXD's
// lower case.
func TestParsePlacement(t *testing.T) {
	got := parsePlacement("e-c1,c5n2,RUNNING,CONTAINER\ne-v1,c5n3,STOPPED,VIRTUAL-MACHINE\n")
	want := map[string]stress.Placement{
		"e-c1": {Member: "c5n2", State: "RUNNING", Type: stress.Container},
		"e-v1": {Member: "c5n3", State: "STOPPED", Type: stress.VM},
	}
	if !maps.Equal(got, want) {
		t.Errorf("placement %v, want %v", got, want)
	}
	if got := parsePlacement(""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

// TestSettled: every instance running on the right side, none missing.
func TestSettled(t *testing.T) {
	p := parsePlacement("e-c1,c5n2,RUNNING,CONTAINER\ne-v1,c5n1,RUNNING,VIRTUAL-MACHINE\n")
	names := []string{"e-c1", "e-v1"}
	if !settled(p, names, "c5n3", false) || settled(p, names, "c5n3", true) {
		t.Error("evacuated instances")
	}
	if settled(p, append(names, "e-c2"), "c5n3", false) {
		t.Error("a missing instance settled")
	}
	p["e-v1"] = stress.Placement{Member: "c5n1", State: "STOPPED"}
	if settled(p, names, "c5n3", false) {
		t.Error("a stopped instance settled")
	}
}

// TestBusyDatasets: LXD's errors as seen on 5.21.8 and 6.9, each dataset
// once.
func TestBusyDatasets(t *testing.T) {
	out := `Error: Failed to evacuate: Failed deleting instance "s-002" in project "default": Failed deleting storage volume: Failed to run: zfs destroy -r default/containers/s-002: exit status 1 (cannot destroy 'default/containers/s-002': dataset is busy)
time="..." level=error msg="..." err="cannot destroy 'default/containers/s-002': dataset is busy"
cannot destroy 'default/virtual-machines/e-v1.block': dataset is busy`
	if got, want := busyDatasets(out), []string{"default/containers/s-002", "default/virtual-machines/e-v1.block"}; !slices.Equal(got, want) {
		t.Errorf("busy %v, want %v", got, want)
	}
	if got := busyDatasets("Error: already exists on migration target storage"); len(got) != 0 {
		t.Errorf("no busy dataset: %v", got)
	}
}

// TestHolders: the script matches the dataset as a mount's source only,
// and its output parses to pid and command line.
func TestHolders(t *testing.T) {
	if s := holdersScript("default/containers/e-c1"); !strings.Contains(s, `' - zfs default/containers/e-c1 '`) {
		t.Errorf("script %s", s)
	}
	out := "holder 4242 /snap/lxd/current/bin/lxd forkfile -- /var/snap/lxd/common/lxd/containers/e-c1 \nnoise\nholder 77 sftp-server\n"
	if got, want := parseHolders(out), []string{"4242 /snap/lxd/current/bin/lxd forkfile -- /var/snap/lxd/common/lxd/containers/e-c1", "77 sftp-server"}; !slices.Equal(got, want) {
		t.Errorf("holders %v, want %v", got, want)
	}
}

// TestLXDErrors: error lines only, bounded.
func TestLXDErrors(t *testing.T) {
	j := "time=x level=info msg=ok\ntime=y level=error msg=\"Failed migrating\"\n" + strings.Repeat("level=error x\n", 30) + strings.Repeat("y", 400) + " dataset is busy\n"
	got := lxdErrors(j)
	if len(got) != 20 || got[0] != `time=y level=error msg="Failed migrating"` {
		t.Errorf("errors %d: %q", len(got), got[0])
	}
	if got := lxdErrors(strings.Repeat("y", 400) + " dataset is busy\n"); len(got) != 1 || len(got[0]) != 300 {
		t.Errorf("long line %v", got)
	}
}

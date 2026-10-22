package acceptance

import (
	"maps"
	"os/exec"
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
// somewhere, as LXD reports it: "cannot destroy 'DATASET': dataset is
// busy" (5.21.8), or, after "zfs destroy -r DATASET", "cannot unmount
// 'MOUNTPOINT': pool or dataset is busy" (6.9).
var busyDataset = regexp.MustCompile(`(?:zfs destroy (?:-r )?([^\s:]+): exit status \d+ \()?cannot (destroy|unmount) '([^']+)': (?:pool or )?dataset is busy`)

// busyDatasets are the datasets out names as busy, once each, in order.
func busyDatasets(out string) []string {
	var ds []string
	for _, m := range busyDataset.FindAllStringSubmatch(out, -1) {
		d := m[1]
		if m[2] == "destroy" {
			d = m[3]
		}
		if d != "" && !slices.Contains(ds, d) {
			ds = append(ds, d)
		}
	}
	return ds
}

// failedMigration is how LXD names an instance whose migration failed:
// in the evacuation's error, and in the members' journals.
var failedMigration = regexp.MustCompile(`Failed migrating instance "([^"]+)"|msg="Failed migration on (?:source|target)" clusterMoveSourceName=(\S+)`)

// failedMigrations are the instances out names as failing to migrate,
// once each, in order.
func failedMigrations(out string) []string {
	var names []string
	for _, m := range failedMigration.FindAllStringSubmatch(out, -1) {
		n := m[1] + m[2]
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// samplerScript samples, once a second for up to 20 minutes, the
// processes of LXD (their command line names fork, sftp or lxd) that run
// in a mount namespace other than LXD's and mount one of the evacuation
// fleet's datasets (STR-73): an LXD helper inside an instance, such as
// the file API's, holds its dataset busy. A dataset still mounted in
// LXD's own namespace is not a holder: an unmount that failed leaves it
// there. A "holder UNIX-TIME PID DATASET COMMAND LINE" line each time.
func samplerScript() string {
	return `lxd=$(pgrep -o -f 'lxd --logfile'); lns=$(readlink /proc/$lxd/ns/mnt); end=$(($(date +%s)+1200)); ` +
		`while [ $(date +%s) -lt $end ]; do for d in /proc/[0-9]*; do ` +
		`ns=$(readlink $d/ns/mnt 2>/dev/null) || continue; [ "$ns" = "$lns" ] && continue; ` +
		`cmd=$(tr '\0' ' ' < $d/cmdline 2>/dev/null); case "$cmd" in *fork*|*sftp*|*lxd*) ;; *) continue;; esac; ` +
		`for ds in $(awk '{for(i=1;i<=NF;i++) if($i=="-"){print $(i+2); break}}' $d/mountinfo 2>/dev/null | grep -E '/(containers|virtual-machines)/([a-z0-9-]+_)?e-[cv][0-9]+(\.block)?$' | sort -u); do ` +
		`echo "holder $(date +%s) ${d#/proc/} $ds $cmd"; done; done; sleep 1; done`
}

// parseSamples reads the sampler's lines into the holders of each
// dataset, as "PID COMMAND LINE (first seen at UNIX-TIME)", once per pid.
func parseSamples(out string) map[string][]string {
	holders := map[string][]string{}
	seen := map[string]bool{}
	for ln := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || f[0] != "holder" || seen[f[2]+" "+f[3]] {
			continue
		}
		seen[f[2]+" "+f[3]] = true
		holders[f[3]] = append(holders[f[3]], strings.TrimSpace(f[2]+" "+strings.Join(f[4:], " "))+" (first seen at "+f[1]+")")
	}
	return holders
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
cannot destroy 'default/virtual-machines/e-v1.block': dataset is busy
Error: Failed migrating instance "e-c1" in project "default": Failed deleting instance on source member: Error deleting storage volume: Failed running: zfs destroy -r default/containers/e-c1: exit status 1 (cannot unmount '/var/snap/lxd/common/lxd/storage-pools/default/containers/e-c1': pool or dataset is busy) (cluster member remains evacuated)`
	if got, want := busyDatasets(out), []string{"default/containers/s-002", "default/virtual-machines/e-v1.block", "default/containers/e-c1"}; !slices.Equal(got, want) {
		t.Errorf("busy %v, want %v", got, want)
	}
	if got := busyDatasets("Error: already exists on migration target storage"); len(got) != 0 {
		t.Errorf("no busy dataset: %v", got)
	}
}

// TestFailedMigrations: the instances LXD names as failing to migrate,
// in the step's output or its journals, once each.
func TestFailedMigrations(t *testing.T) {
	out := `Error: Failed migrating instance "e-c1" in project "default": Failed deleting instance on source member
time="x" level=error msg="Failed migration on source" clusterMoveSourceName=e-c1 err="..."
time="x" level=error msg="Failed migration on target" clusterMoveSourceName=e-v2 err="..."`
	if got, want := failedMigrations(out), []string{"e-c1", "e-v2"}; !slices.Equal(got, want) {
		t.Errorf("failed %v, want %v", got, want)
	}
	if got := failedMigrations("Error: timeout"); len(got) != 0 {
		t.Errorf("none named: %v", got)
	}
}

// TestSamples: the sampler's script is valid shell; its lines parse to
// each dataset's holders, once per pid.
func TestSamples(t *testing.T) {
	if out, err := exec.Command("sh", "-n", "-c", samplerScript()).CombinedOutput(); err != nil {
		t.Errorf("sampler script: %v %s", err, out)
	}
	out := "holder 100 4242 default/containers/e-c1 /snap/lxd/40916/bin/lxd forkfile -- 3 \n" +
		"holder 101 4242 default/containers/e-c1 /snap/lxd/40916/bin/lxd forkfile -- 3\nnoise\n" +
		"holder 101 77 default/containers/e-c2 sftp-server\n"
	got := parseSamples(out)
	want := map[string][]string{
		"default/containers/e-c1": {"4242 /snap/lxd/40916/bin/lxd forkfile -- 3 (first seen at 100)"},
		"default/containers/e-c2": {"77 sftp-server (first seen at 101)"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
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

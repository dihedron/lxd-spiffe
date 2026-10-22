package stress

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSTR72Arms: the arms alternate in the order given, each as many
// times as asked; the default is idle, files, files-vm, and an unknown
// arm is refused.
func TestSTR72Arms(t *testing.T) {
	if got, want := EvacArms(2, []string{ArmIdle, ArmFiles}), []string{ArmIdle, ArmFiles, ArmIdle, ArmFiles}; !slices.Equal(got, want) {
		t.Errorf("arms %v, want %v", got, want)
	}
	if got := EvacArms(0, []string{ArmIdle}); len(got) != 0 {
		t.Errorf("no cycles: %v", got)
	}
	if got, err := ParseEvacArms(""); err != nil || !slices.Equal(got, []string{ArmIdle, ArmFiles, ArmFilesVM}) {
		t.Errorf("default arms %v %v", got, err)
	}
	if got, err := ParseEvacArms(" files-vm "); err != nil || !slices.Equal(got, []string{ArmFilesVM}) {
		t.Errorf("one arm %v %v", got, err)
	}
	for _, bad := range []string{"files,vm", "idle,,files", "idle,idle"} {
		if _, err := ParseEvacArms(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

var evacTypes = map[string]string{"e-c1": Container, "e-c2": Container, "e-v1": VM, "e-v2": VM}

// placement places every instance of evacTypes on member, running, but
// those in moved, which go to other.
func placement(member, other string, moved ...string) map[string]Placement {
	p := map[string]Placement{}
	for n, typ := range evacTypes {
		m := member
		if slices.Contains(moved, n) {
			m = other
		}
		p[n] = Placement{Member: m, State: "RUNNING", Type: typ}
	}
	return p
}

// cycle is a cycle on c5n3 whose evacuation moved every instance to c5n2
// and whose restore brought them back.
func cycle(n int, arm string) EvacCycle {
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * 5 * time.Minute)
	return EvacCycle{
		N: n, Server: "c5", Version: "5.21.8", Arm: arm, Evacuee: "c5n3", Types: evacTypes,
		Start: start, End: start.Add(4 * time.Minute),
		Evacuate: EvacStep{OK: true, Tries: 1, DurationMS: 60000 + int64(n)*1000, Placement: placement("c5n2", "")},
		Restore:  EvacStep{OK: true, Tries: 1, DurationMS: 90000, Placement: placement("c5n3", "")},
	}
}

// TestSTR75Migrations: every instance attempts one migration per step;
// one that is not where the step should leave it, or not running, failed;
// volumes left behind and busy datasets are counted by instance type.
func TestSTR75Migrations(t *testing.T) {
	bad := cycle(1, ArmFiles)
	// e-c1 stayed on the evacuee, its volume left behind on c5n3 after the
	// restore failed three times
	bad.Evacuate.OK = false
	bad.Evacuate.Error = "Failed deleting instance \"e-c1\": cannot destroy 'default/containers/e-c1': dataset is busy"
	bad.Evacuate.Placement = placement("c5n2", "c5n3", "e-c1")
	bad.Evacuate.Failed = []string{"e-c1"}
	bad.Evacuate.Busy = []string{"default/containers/e-c1"}
	bad.Evacuate.Holders = map[string][]string{"default/containers/e-c1": {"4242 sftp-server"}}
	bad.Evacuate.Orphans = []string{"e-c2@c5n3"}
	// the blocked restore brought none back: e-c1 never left, the others
	// failed their restore
	bad.Restore = EvacStep{Tries: 3, DurationMS: 90000, Error: "already exists on migration target storage", Placement: placement("c5n2", "c5n3", "e-c1"),
		Failed: []string{"e-c2", "e-v1", "e-v2"}}
	bad.Blocked, bad.Reset = true, true
	stopped := cycle(2, ArmIdle)
	// a VM back on the evacuee but stopped
	p := stopped.Restore.Placement["e-v2"]
	p.State = "STOPPED"
	stopped.Restore.Placement["e-v2"] = p

	s := SummarizeEvac([]EvacCycle{cycle(0, ArmIdle), bad, stopped, cycle(3, ArmFiles)}, map[string]string{"cluster": "c5"})
	if !s.OK() {
		t.Fatalf("no harness error, but not OK: %v", s.HarnessErrors)
	}
	get := func(arm, typ string) EvacGroup {
		for _, g := range s.Groups {
			if g.Version == "5.21.8" && g.Arm == arm && g.Type == typ {
				return g
			}
		}
		t.Fatalf("no group %s/%s", arm, typ)
		return EvacGroup{}
	}
	// a restore is attempted only for the instances the evacuation moved
	if g := get(ArmFiles, Container); g.EvacuateAttempted != 4 || g.EvacuateFailed != 1 || g.RestoreAttempted != 3 || g.RestoreFailed != 1 ||
		g.VolumesLeft != 1 || g.BusyErrors != 1 {
		t.Errorf("files/container %+v", g)
	}
	if g := get(ArmIdle, VM); g.EvacuateFailed != 0 || g.RestoreFailed != 1 || g.RestoreAttempted != 4 {
		t.Errorf("idle/vm %+v", g)
	}
	if g := get(ArmFiles, VM); g.EvacuateFailed != 0 || g.RestoreAttempted != 4 || g.RestoreFailed != 2 || g.VolumesLeft+g.BusyErrors != 0 {
		t.Errorf("files/vm %+v", g)
	}
	var files EvacCycles
	for _, c := range s.Cycles {
		if c.Arm == ArmFiles {
			files = c
		}
	}
	if files.Cycles != 2 || files.Blocked != 1 || files.Evacuate.Count != 2 || files.Evacuate.Max != 63000 || files.Restore.P50 != 90000 {
		t.Errorf("files cycles %+v", files)
	}
	if got := s.Holders; len(got) != 1 || got[0] != "cycle 1, c5n3, default/containers/e-c1: 4242 sftp-server" {
		t.Errorf("holders %v", got)
	}
}

// TestSTR75FailedSteps: a step that ended with an error, its instances
// where they should be, counts the error but no failed migration.
func TestSTR75FailedSteps(t *testing.T) {
	c := cycle(0, ArmIdle)
	c.Restore.OK, c.Restore.Error = false, "timeout"
	s := SummarizeEvac([]EvacCycle{c}, nil)
	if s.Cycles[0].RestoreErrors != 1 || s.Cycles[0].EvacuateErrors != 0 {
		t.Errorf("step errors %+v", s.Cycles[0])
	}
	for _, g := range s.Groups {
		if g.RestoreFailed != 0 {
			t.Errorf("instances in place counted as failed: %+v", g)
		}
	}
}

// TestSTR75AbortedStep: LXD stops an evacuation at its first failed
// migration; the instances it never tried, still running on the evacuee,
// are not attempted, not failed.
func TestSTR75AbortedStep(t *testing.T) {
	c := cycle(0, ArmFiles)
	c.Evacuate.OK, c.Evacuate.Failed = false, []string{"e-c1"}
	c.Evacuate.Placement = placement("c5n3", "c5n1", "e-c1")
	stopped := c.Evacuate.Placement["e-c1"]
	stopped.State = "STOPPED"
	c.Evacuate.Placement["e-c1"] = stopped
	c.Restore = EvacStep{Failed: []string{"e-c1"}, Placement: c.Evacuate.Placement}
	c.Blocked = true
	s := SummarizeEvac([]EvacCycle{c}, nil)
	for _, g := range s.Groups {
		switch g.Type {
		case Container:
			if g.EvacuateAttempted != 1 || g.EvacuateFailed != 1 || g.EvacuateSkipped != 1 || g.RestoreAttempted != 0 {
				t.Errorf("containers %+v", g)
			}
		case VM:
			if g.EvacuateAttempted != 0 || g.EvacuateSkipped != 2 || g.EvacuateFailed != 0 {
				t.Errorf("VMs %+v", g)
			}
		}
	}
	// a restore that stops early leaves the instances it never tried
	// elsewhere: skipped too
	r := cycle(1, ArmIdle)
	r.Restore.OK, r.Restore.Failed = false, []string{"e-v1"}
	r.Restore.Placement = placement("c5n2", "")
	s = SummarizeEvac([]EvacCycle{r}, nil)
	for _, g := range s.Groups {
		if g.Type == VM && (g.RestoreAttempted != 1 || g.RestoreFailed != 1 || g.RestoreSkipped != 1) {
			t.Errorf("VM restores %+v", g)
		}
		if g.Type == Container && (g.RestoreAttempted != 0 || g.RestoreSkipped != 2) {
			t.Errorf("container restores %+v", g)
		}
	}
}

// TestSTR76Verdict: only harness errors fail ST-6; the bound on the
// failure rate is the rule of three without failures, the observed rate
// with them.
func TestSTR76Verdict(t *testing.T) {
	broken := cycle(1, ArmFiles)
	broken.HarnessError = "lab.sh reset failed"
	s := SummarizeEvac([]EvacCycle{cycle(0, ArmIdle), broken}, nil)
	if s.OK() || len(s.HarnessErrors) != 1 || !strings.Contains(s.HarnessErrors[0], "lab.sh reset failed") {
		t.Errorf("harness errors %v", s.HarnessErrors)
	}
	// a cycle the harness could not run counts no migration
	for _, g := range s.Groups {
		if g.Arm == ArmFiles {
			t.Errorf("group of a broken cycle: %+v", g)
		}
	}
	if got := (EvacGroup{EvacuateAttempted: 30, RestoreAttempted: 30}).Rate(); got != "below 5.0 % (none failed of 60)" {
		t.Errorf("rate %q", got)
	}
	if got := (EvacGroup{EvacuateAttempted: 30, RestoreAttempted: 30, EvacuateFailed: 2, RestoreFailed: 1}).Rate(); got != "5.0 % (3 failed of 60)" {
		t.Errorf("rate %q", got)
	}
	if got := (EvacGroup{}).Rate(); got != "n/a" {
		t.Errorf("rate %q", got)
	}
}

// TestSTR75Report: summary.md names the groups, the cycles and the
// holders; summary.json and results.jsonl decode.
func TestSTR75Report(t *testing.T) {
	c := cycle(0, ArmFiles)
	c.Evacuate.Busy = []string{"default/virtual-machines/e-v1.block"}
	c.Evacuate.Holders = map[string][]string{"default/virtual-machines/e-v1.block": nil}
	s := SummarizeEvac([]EvacCycle{c}, map[string]string{"cluster": "c5", "cycles": "1"})
	var md bytes.Buffer
	if err := s.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Evacuation reliability summary", "**No harness error.** 1 cycles, 0 blocked.", "- `cluster` = `c5`",
		"| 5.21.8 | files | container | 0 of 2 | 0 of 2 | 0 | 0 | 0 | below 75.0 % (none failed of 4) |",
		"| 5.21.8 | files | virtual-machine | 0 of 2 | 0 of 2 | 0 | 0 | 1 |",
		"| 5.21.8 | files | 1 | 0 | 0 | 0 | 60000 ms | 60000 ms | 90000 ms | 90000 ms |",
		"cycle 0, c5n3, default/virtual-machines/e-v1.block: no holder found",
	} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("summary.md lacks %q:\n%s", want, md.String())
		}
	}
	var js bytes.Buffer
	if err := s.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	var back EvacSummary
	if err := json.Unmarshal(js.Bytes(), &back); err != nil || len(back.Groups) != 2 {
		t.Errorf("summary.json: %v, %+v", err, back)
	}
	var lines bytes.Buffer
	if err := WriteCycles(&lines, []EvacCycle{c, c}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(lines.String(), "\n"); n != 2 {
		t.Errorf("results.jsonl has %d lines", n)
	}
}

// TestDatasetInstance: the instance a ZFS dataset belongs to, in the
// default project or another.
func TestDatasetInstance(t *testing.T) {
	for in, want := range map[string]string{
		"default/containers/e-c1":                 "e-c1",
		"default/virtual-machines/e-v1.block":     "e-v1",
		"default/virtual-machines/e-v1":           "e-v1",
		"default/containers/team_e-c1":            "e-c1",
		"default/containers/e-c1@snapshot-backup": "e-c1",
	} {
		if got := DatasetInstance(in); got != want {
			t.Errorf("DatasetInstance(%q) = %q, want %q", in, got, want)
		}
	}
}

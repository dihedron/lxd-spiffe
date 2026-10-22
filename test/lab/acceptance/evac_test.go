//go:build lab

package acceptance

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/test/lab/stress"
)

// ST-6 (stress spec, Evacuation reliability): cycles of evacuate and
// restore of one member, the evacuee, which holds Alpine containers and
// VMs, idle or under file-API reads; a characterisation, failing only on
// a harness error (STR-76).

// evacRun is one cluster's ST-6 session.
type evacRun struct {
	l       *lab
	cluster string
	entry   string
	evacuee string
	version string
	names   []string
	types   map[string]string
}

// evacReader marks the files arm's readers on the entry member, and
// evacSampler the holder samplers on every member; evacReaders and
// evacSamplers match them for pkill -f but not the command that runs it.
const (
	evacReader   = "evac-reader"
	evacReaders  = "'[e]vac-reader'"
	evacSampler  = "evac-sampler"
	evacSamplers = "'[e]vac-sampler'"
)

// newEvacRun picks the evacuee: neither the entry member nor the one
// holding the baseline VM (STR-70).
func newEvacRun(t *testing.T, l *lab, cluster string, containers, vms int) *evacRun {
	t.Helper()
	er := &evacRun{l: l, cluster: cluster, entry: l.vmOf(cluster), version: l.env.Servers[cluster].Version, types: map[string]string{}}
	vm := strings.TrimSpace(l.ssh(t, cluster, boundedRemote(30*time.Second, "lxc list -c L -f csv '^v-ubuntu$'")))
	for _, m := range l.members(cluster) {
		if m != er.entry && m != vm {
			er.evacuee = m
			break
		}
	}
	if er.evacuee == "" {
		t.Fatalf("%s: no member is free to evacuate (entry %s, v-ubuntu on %q)", cluster, er.entry, vm)
	}
	for i := 1; i <= containers; i++ {
		er.names = append(er.names, fmt.Sprintf("e-c%d", i))
		er.types[er.names[len(er.names)-1]] = stress.Container
	}
	for i := 1; i <= vms; i++ {
		er.names = append(er.names, fmt.Sprintf("e-v%d", i))
		er.types[er.names[len(er.names)-1]] = stress.VM
	}
	return er
}

// remote runs a bounded command on the entry member, its standard error
// with its output.
func (er *evacRun) remote(d time.Duration, command string) (string, error) {
	return er.l.trySSH(er.entry, boundedRemote(d, "{ "+command+"; } 2>&1"))
}

// teardown deletes the evacuation fleet and stops any reader (STR-02).
func (er *evacRun) teardown() error {
	for _, m := range er.l.members(er.cluster) {
		_, _ = er.l.trySSH(m, "sudo pkill -f "+evacSamplers)
	}
	_, err := er.remote(5*time.Minute, `pkill -f `+evacReaders+`; for i in $(lxc list -c n -f csv '^e-[cv][0-9]+$'); do lxc delete --force "$i"; done; true`)
	return err
}

// build launches the evacuation fleet on the evacuee, each instance tried
// up to three times, and waits until all run there (STR-70).
func (er *evacRun) build() error {
	if err := er.teardown(); err != nil {
		return fmt.Errorf("deleting an earlier fleet: %w", err)
	}
	if orphans, err := er.orphans(); err != nil || len(orphans) > 0 {
		return fmt.Errorf("instance volumes without their instance (lab.sh reset): %v %w", orphans, err)
	}
	vmFlags := " --vm -c limits.cpu=1 -c limits.memory=384MiB -c " + er.nosecureboot()
	for _, n := range er.names {
		launch := "lxc launch " + stressImage + " " + n + " --target " + er.evacuee + " -q"
		if er.types[n] == stress.VM {
			launch = "lxc launch " + setting("LAB_IMAGE_ALPINE", "images:alpine/3.24/cloud") + " " + n + " --target " + er.evacuee + vmFlags + " -q"
		}
		var out string
		var err error
		for range 3 {
			if out, err = er.remote(10*time.Minute, launch); err == nil {
				break
			}
			_, _ = er.remote(2*time.Minute, "lxc delete --force "+n)
		}
		if err != nil {
			return fmt.Errorf("launching %s: %w %s", n, err, lastLine(out))
		}
	}
	if !poll(5*time.Minute, func() bool { p, err := er.placement(); return err == nil && settled(p, er.names, er.evacuee, true) }) {
		return fmt.Errorf("the fleet does not run on %s", er.evacuee)
	}
	return nil
}

// nosecureboot turns secure boot off for the Alpine VMs, as the lab's
// baseline does: LXD 6 moved it into boot.mode (instance_boot_mode).
func (er *evacRun) nosecureboot() string {
	if _, err := er.remote(30*time.Second, `lxc query /1.0 | grep -q '"instance_boot_mode"'`); err == nil {
		return "boot.mode=uefi-nosecureboot"
	}
	return "security.secureboot=false"
}

func (er *evacRun) placement() (map[string]stress.Placement, error) {
	out, err := er.l.trySSH(er.entry, boundedRemote(30*time.Second, "lxc list -c nLst -f csv '^e-[cv][0-9]+$'"))
	return parsePlacement(out), err
}

// orphans are the cluster's instance volumes whose instance is not on
// their member.
func (er *evacRun) orphans() ([]string, error) {
	vols, err := er.l.trySSH(er.entry, boundedRemote(30*time.Second, "lxc storage volume list default -f csv"))
	if err != nil {
		return nil, err
	}
	insts, err := er.l.trySSH(er.entry, boundedRemote(30*time.Second, "lxc list -c nL -f csv"))
	if err != nil {
		return nil, err
	}
	return orphanVolumes(vols, insts), nil
}

// poll checks every 5 s until check holds or d has passed.
func poll(d time.Duration, check func() bool) bool {
	for end := time.Now().Add(d); ; time.Sleep(5 * time.Second) {
		if check() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// startStep starts what runs during a step: on every member the holder
// sampler (STR-73), and in the files arm the readers (startReaders). The
// returned function stops them and records the holders and the reads.
func (er *evacRun) startStep(arm string) (stop func(*stress.EvacStep), err error) {
	script := "rm -f /tmp/evac-holders.log; setsid nohup sh -c " + shellQuote(samplerScript()) + " " + evacSampler + " >/tmp/evac-holders.log 2>&1 </dev/null &"
	for _, m := range er.l.members(er.cluster) {
		if _, err := er.l.trySSH(m, "sudo sh -c "+shellQuote(script)); err != nil {
			return nil, fmt.Errorf("starting the holder sampler on %s: %w", m, err)
		}
	}
	stopReaders, err := er.startReaders(arm)
	if err != nil {
		return nil, err
	}
	return func(s *stress.EvacStep) {
		stopReaders(s)
		for _, m := range er.l.members(er.cluster) {
			out, _ := er.l.trySSH(m, boundedRemote(time.Minute, "pkill -f "+evacSamplers+`; awk '!seen[$3" "$4]++' /tmp/evac-holders.log`))
			for ds, hs := range parseSamples(out) {
				if s.Holders == nil {
					s.Holders = map[string][]string{}
				}
				for _, h := range hs {
					s.Holders[ds] = append(s.Holders[ds], m+": "+h)
				}
			}
		}
	}, nil
}

// startReaders starts, in the files arms, one reader per instance (per VM
// in files-vm) on the entry member, pulling /etc/hostname through the
// files API one request at a time, as file_pull does (STR-72), on LXD's
// local socket (lxc query wants JSON, the files API answers the file);
// each stops by itself after 20 minutes. The returned function stops them
// and counts their reads.
func (er *evacRun) startReaders(arm string) (stop func(*stress.EvacStep), err error) {
	names := er.read(arm)
	if len(names) == 0 {
		return func(*stress.EvacStep) {}, nil
	}
	reader := `end=$(($(date +%s)+1200)); while [ $(date +%s) -lt $end ]; do ` +
		`if curl -sf -m 30 -o /dev/null --unix-socket /var/snap/lxd/common/lxd/unix.socket "http://lxd/1.0/instances/$1/files?path=/etc/hostname"; then echo o; else echo f; sleep 1; fi; ` +
		`done >>/tmp/evac-reads-$1`
	script := "rm -f /tmp/evac-reads-*; for n in " + strings.Join(names, " ") + "; do " +
		"setsid nohup sh -c " + shellQuote(reader) + " " + evacReader + ` "$n" >/dev/null 2>&1 </dev/null & done`
	if _, err := er.l.trySSH(er.entry, "sudo sh -c "+shellQuote(script)); err != nil {
		return nil, fmt.Errorf("starting the readers: %w", err)
	}
	return func(s *stress.EvacStep) {
		out, _ := er.remote(time.Minute, `pkill -f `+evacReaders+`; for f in /tmp/evac-reads-*; do echo "reads $f $(grep -c o "$f") $(grep -c f "$f")"; done`)
		s.Reads, s.ReadErrors = parseReads(out)
	}, nil
}

// read is the instances the arm reads files of.
func (er *evacRun) read(arm string) []string {
	var names []string
	for _, n := range er.names {
		if arm == stress.ArmFiles || arm == stress.ArmFilesVM && er.types[n] == stress.VM {
			names = append(names, n)
		}
	}
	return names
}

// inspect records, after a step started at since, LXD's errors on every
// member, the datasets it found busy, and the instances it names as
// failing to migrate (STR-71 step 3).
func (er *evacRun) inspect(s *stress.EvacStep, since time.Time, output string) {
	journals := output
	for _, m := range er.l.members(er.cluster) {
		j, _ := er.l.trySSH(m, boundedRemote(time.Minute, fmt.Sprintf("journalctl -u snap.lxd.daemon --since @%d --no-pager -o cat", since.Unix())))
		for _, e := range lxdErrors(j) {
			s.Errors = append(s.Errors, m+": "+e)
		}
		journals += "\n" + j
	}
	s.Busy = busyDatasets(journals)
	s.Failed = failedMigrations(journals)
}

// settle waits, after a step that succeeded, up to 5 minutes for the
// instances to run where it should leave them (and, after a restore, for
// the cluster's health); LXD stops a failed step, so nothing is waited
// for. Then it records where the instances are and the orphaned volumes.
func (er *evacRun) settle(s *stress.EvacStep, onEvacuee bool) error {
	if s.OK {
		poll(5*time.Minute, func() bool {
			p, err := er.placement()
			return err == nil && settled(p, er.names, er.evacuee, onEvacuee) && (!onEvacuee || er.healthy())
		})
	}
	var err error
	if s.Placement, err = er.placement(); err != nil {
		return fmt.Errorf("listing the fleet: %w", err)
	}
	if s.Orphans, err = er.orphans(); err != nil {
		return fmt.Errorf("listing the volumes: %w", err)
	}
	return nil
}

// evacuate is step 2: the evacuee evacuated (--force on 5.21, --yes on
// 6.x, gate L8), then up to 5 minutes for its instances to run elsewhere.
func (er *evacRun) evacuate(arm string) (stress.EvacStep, error) {
	flag := "--yes"
	if strings.HasPrefix(er.version, "5.") {
		flag = "--force"
	}
	stop, err := er.startStep(arm)
	if err != nil {
		return stress.EvacStep{}, err
	}
	since := time.Now()
	out, err := er.remote(15*time.Minute, "lxc cluster evacuate "+er.evacuee+" "+flag)
	s := stress.EvacStep{OK: err == nil, Tries: 1, DurationMS: time.Since(since).Milliseconds()}
	stop(&s)
	if err != nil {
		s.Error = lastLine(out)
	}
	er.inspect(&s, since, out)
	return s, er.settle(&s, false)
}

// restore is step 4: the evacuee restored, tried again for up to 5
// minutes; three failures on a leftover volume block the cycle (STR-74);
// then up to 5 minutes for the cluster's health and the instances to run
// on the evacuee again.
func (er *evacRun) restore(arm string) (s stress.EvacStep, blocked bool, err error) {
	stop, err := er.startStep(arm)
	if err != nil {
		return s, false, err
	}
	since := time.Now()
	var outputs []string
	leftovers := 0
	for end := since.Add(5 * time.Minute); ; time.Sleep(10 * time.Second) {
		s.Tries++
		out, err := er.remote(10*time.Minute, "lxc cluster restore "+er.evacuee+" --force")
		outputs = append(outputs, out)
		if err == nil || strings.Contains(out, "already restored") {
			s.OK, s.Error = true, ""
			break
		}
		s.Error = lastLine(out)
		if strings.Contains(out, leftoverVolume) {
			if leftovers++; leftovers == 3 {
				blocked = true
				break
			}
		}
		if time.Now().After(end) {
			break
		}
	}
	s.DurationMS = time.Since(since).Milliseconds()
	stop(&s)
	er.inspect(&s, since, strings.Join(outputs, "\n"))
	return s, blocked, er.settle(&s, true)
}

// quiet waits up to 3 minutes until no LXD operation runs on the cluster
// and every VM's agent answers: a VM still in its first-boot restart
// refused its migration ("busy running a restart operation", smoke on
// 6.9). It reports what it waited for in vain.
func (er *evacRun) quiet() string {
	var vms []string
	for _, n := range er.names {
		if er.types[n] == stress.VM {
			vms = append(vms, n)
		}
	}
	check := `lxc operation list -f csv | grep -q ',RUNNING,' && echo operations; for v in ` + strings.Join(vms, " ") +
		`; do timeout -k 5 20 lxc exec "$v" -- true >/dev/null 2>&1 || echo "$v"; done; true`
	var last string
	if poll(3*time.Minute, func() bool {
		out, err := er.remote(2*time.Minute, check)
		last = strings.Join(strings.Fields(out), " ")
		return err == nil && last == ""
	}) {
		return ""
	}
	return "not quiet after 3 minutes: " + last
}

// healthy reports whether every member is ONLINE and a database voter
// (ST-4): on 6.9 a member restored from an evacuation regains its role
// after a while.
func (er *evacRun) healthy() bool {
	out, err := er.l.trySSH(er.entry, "sudo timeout -k 5 10 lxc cluster list -f csv")
	return err == nil && clusterHealthy(out, len(er.l.members(er.cluster)))
}

// putBack brings the fleet back in place after a cycle that left it
// otherwise without being blocked: an instance stopped is started, one
// elsewhere moved back. It returns what it did, or an error when the fleet
// is still not in place.
func (er *evacRun) putBack(p map[string]stress.Placement) ([]string, error) {
	var notes []string
	for _, n := range er.names {
		pl := p[n]
		switch {
		case pl.Member != er.evacuee:
			out, err := er.remote(10*time.Minute, fmt.Sprintf("lxc stop --force %s; lxc move %s --target %s && lxc start %s", n, n, er.evacuee, n))
			notes = append(notes, fmt.Sprintf("%s moved back from %q: %v %s", n, pl.Member, err, lastLine(out)))
		case pl.State != "RUNNING":
			out, err := er.remote(5*time.Minute, "lxc start "+n)
			notes = append(notes, fmt.Sprintf("%s started (was %s): %v %s", n, pl.State, err, lastLine(out)))
		}
	}
	ok := poll(3*time.Minute, func() bool { p, err := er.placement(); return err == nil && settled(p, er.names, er.evacuee, true) })
	orphans, err := er.orphans()
	if !ok || err != nil || len(orphans) > 0 {
		return notes, fmt.Errorf("the fleet is not back in place (orphans %v, %v)", orphans, err)
	}
	return notes, nil
}

// reset is STR-74's repair: the cluster back to its snapshot (lab.sh
// reset, which also resets the standalone servers), the fleet rebuilt.
func (er *evacRun) reset() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, filepath.Join(er.l.dir, "lab.sh"), "reset").CombinedOutput(); err != nil {
		return fmt.Errorf("lab.sh reset: %w\n%s", err, lastLine(string(out)))
	}
	return er.build()
}

// cycle runs one cycle (STR-71): the fleet checked in place, the evacuee
// evacuated and restored with the records of each step, the fleet put
// back, or the cluster reset when the cycle is blocked, the fleet cannot
// be put back, or the cluster is not healthy again.
func (er *evacRun) cycle(n int, arm string) (c stress.EvacCycle) {
	c = stress.EvacCycle{N: n, Server: er.cluster, Version: er.version, Arm: arm, Evacuee: er.evacuee, Types: er.types, Start: time.Now()}
	defer func() { c.End = time.Now() }()
	if q := er.quiet(); q != "" {
		c.Notes = append(c.Notes, q)
	}
	p, err := er.placement()
	orphans, err2 := er.orphans()
	if err != nil || err2 != nil || !settled(p, er.names, er.evacuee, true) || len(orphans) > 0 {
		c.HarnessError = fmt.Sprintf("the fleet is not in place before the cycle: %v, orphans %v (%v, %v)", p, orphans, err, err2)
		return c
	}
	if c.Evacuate, err = er.evacuate(arm); err != nil {
		c.HarnessError = err.Error()
		return c
	}
	if c.Restore, c.Blocked, err = er.restore(arm); err != nil {
		c.HarnessError = err.Error()
		return c
	}
	if !c.Blocked && !er.healthy() {
		c.Notes = append(c.Notes, "the cluster is not healthy after the restore")
	} else if !c.Blocked {
		if settled(c.Restore.Placement, er.names, er.evacuee, true) && len(c.Restore.Orphans) == 0 {
			return c
		}
		notes, err := er.putBack(c.Restore.Placement)
		if c.Notes = notes; err == nil {
			return c
		}
		c.Notes = append(c.Notes, err.Error())
	}
	c.Reset = true
	if err := er.reset(); err != nil {
		c.HarnessError = err.Error()
	}
	return c
}

// TestST6Evacuation is ST-6: on each running cluster in turn, cycles of
// evacuate and restore of the evacuee, idle and files arms alternating.
// LAB_EVAC_CONTAINERS (default 3), LAB_EVAC_VMS (default 3) and
// LAB_EVAC_CYCLES (default 10 per arm) size it. A blocked cycle resets
// the lab (lab.sh reset, standalone servers included): deploy again
// before other sessions.
func TestST6Evacuation(t *testing.T) {
	l := newLab(t)
	clusters := l.clusters()
	if len(clusters) == 0 {
		t.Skip("ST-6 needs a running cluster: LAB_CLUSTER=5.21, 6.9 or both")
	}
	count := func(name, def string) int {
		n, err := strconv.Atoi(setting(name, def))
		if err != nil || n < 0 {
			t.Fatalf("%s: not a count", name)
		}
		return n
	}
	containers, vms, cycles := count("LAB_EVAC_CONTAINERS", "3"), count("LAB_EVAC_VMS", "3"), count("LAB_EVAC_CYCLES", "10")
	if containers+vms == 0 || cycles == 0 {
		t.Fatal("ST-6 needs instances and cycles: check LAB_EVAC_*")
	}
	arms, err := stress.ParseEvacArms(setting("LAB_EVAC_ARMS", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, cluster := range clusters {
		t.Run(cluster, func(t *testing.T) {
			er := newEvacRun(t, l, cluster, containers, vms)
			t.Cleanup(func() {
				if err := er.teardown(); err != nil {
					t.Errorf("deleting the fleet: %v", err)
				}
			})
			start := time.Now()
			if err := er.build(); err != nil {
				t.Fatalf("%s: building the evacuation fleet on %s: %v", cluster, er.evacuee, err)
			}
			t.Logf("%s: %d containers and %d VMs on %s in %s", cluster, containers, vms, er.evacuee, time.Since(start).Round(time.Second))
			stamp := time.Now().UTC().Format("20060102T150405Z")
			var out []stress.EvacCycle
			for n, arm := range stress.EvacArms(cycles, arms) {
				c := er.cycle(n, arm)
				out = append(out, c)
				t.Logf("cycle %d (%s): evacuate ok %t in %d ms, restore ok %t in %d ms (%d tries), orphans %v, busy %v, blocked %t, reset %t %s",
					n, arm, c.Evacuate.OK, c.Evacuate.DurationMS, c.Restore.OK, c.Restore.DurationMS, c.Restore.Tries,
					append(c.Evacuate.Orphans, c.Restore.Orphans...), append(c.Evacuate.Busy, c.Restore.Busy...), c.Blocked, c.Reset, strings.Join(c.Notes, "; "))
				if c.HarnessError != "" {
					t.Logf("cycle %d: %s; the session stops", n, c.HarnessError)
					break
				}
			}
			harness, _ := exec.Command("git", "-C", l.repo, "describe", "--always", "--dirty").Output()
			settings := map[string]string{
				"session": "ST-6", "cluster": cluster, "version": er.version, "evacuee": er.evacuee, "harness": strings.TrimSpace(string(harness)),
				"containers": strconv.Itoa(containers), "vms": strconv.Itoa(vms), "cycles": strconv.Itoa(cycles),
				"arms": strings.Join(arms, ","),
			}
			sum := stress.SummarizeEvac(out, settings)
			l.writeRun(t, stamp+"-st-6-"+cluster+"-summary", sum.OK(), sum.WriteMarkdown, sum.WriteJSON,
				func(w io.Writer) error { return stress.WriteCycles(w, out) })
			if !sum.OK() {
				t.Errorf("harness errors: %v", sum.HarnessErrors)
			}
		})
	}
}

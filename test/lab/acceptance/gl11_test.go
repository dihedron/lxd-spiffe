//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleL11 is what gate L11 decides, in short.
const ruleL11 = "L11 decision: whether forwarding is transparent (reads, files, PATCH with If-Match, the filtered list); " +
	"the accuracy of location, also after a move and an evacuation; whether volatile.uuid and the generation survive " +
	"both; what an attestation sees in flight (FR-S5); the differences between 5.21 (baseline) and 6.x."

// l11Instances are the cluster baseline instances: c-unpriv on the first
// member, v-ubuntu on the second.
var l11Instances = []string{"c-unpriv", "v-ubuntu"}

// TestGL11Cluster is G-L11, the L11 runbook (probe spec) on each running
// cluster. Through each member, one run per member (a run records one
// endpoint), with --record: instance show and file stat of both
// instances, config set and unset with PATCH and If-Match, and the
// filtered list through the third member, which holds neither. Then, in a
// run through the first member, with instance watch on both instances for
// LAB_L11_WATCH (default 20m): c-unpriv stopped, moved to the third member
// and started; the second member evacuated and restored. While each
// operation runs, and after start, evacuation and restore until the
// instance answers again, instance show and
// file stat of the instance read it every LAB_L11_READ_EVERY (default 5s),
// so that the reads can be set against what the watch alone saw. Live
// migration and the snap/deb differences are recorded as notes. The
// cluster is reset before and after unless LAB_L11_RESET=no; with
// LAB_COLLECT=yes the runs go to docs/validation/ and the member runs'
// fixtures to test/fixtures/<version>/cluster/<member>/.
func TestGL11Cluster(t *testing.T) {
	l := newLab(t)
	clusters := l.clusters()
	if len(clusters) == 0 {
		t.Skip("G-L11 needs a cluster: LAB_CLUSTER=5.21 or 6.9")
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	watchFor, err := time.ParseDuration(setting("LAB_L11_WATCH", "20m"))
	if err != nil {
		t.Fatalf("LAB_L11_WATCH: %v", err)
	}
	readEvery, err := time.ParseDuration(setting("LAB_L11_READ_EVERY", "5s"))
	if err != nil {
		t.Fatalf("LAB_L11_READ_EVERY: %v", err)
	}
	if setting("LAB_L11_RESET", "yes") == "yes" {
		l.labsh(t, "reset")
		t.Cleanup(func() {
			cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "reset")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("lab.sh reset: %v\n%s", err, out)
			}
		})
	}
	for _, cluster := range clusters {
		t.Run(cluster, func(t *testing.T) {
			members := l.members(cluster)
			if len(members) < 3 {
				t.Fatalf("cluster %s has %d members, G-L11 needs 3", cluster, len(members))
			}
			uuids := map[string]string{}
			for _, name := range l11Instances {
				uuids[name] = strings.TrimSpace(l.ssh(t, members[0], "sudo lxc config get "+name+" volatile.uuid"))
			}
			var runs []string
			for i, member := range members {
				runs = append(runs, l.gl11Forwarding(t, cluster, member, stamp, i+1, uuids, member == members[2]))
			}
			moves := l.gl11Moves(t, cluster, members, stamp, watchFor, readEvery)

			version := l.env.Servers[cluster].Version
			for _, runID := range append(runs, moves) {
				runDir := filepath.Join(l.evidence, runID)
				staging := filepath.Join(t.TempDir(), runID)
				l.sanitize(t, runDir, staging)
				l.sanitizeCheck(t, staging)
				t.Logf("run %s, in %s", runID, runDir)
				if collect && !t.Failed() {
					l.collectRun(t, runID, staging)
					if runID != moves {
						member := strings.TrimPrefix(runID, stamp+"-g-l11-"+cluster+"-")
						l.collectFixtures(t, cluster, "cluster", member, staging)
					}
				}
			}
			t.Logf("LXD %s", version)
			t.Log(ruleL11)
		})
	}
}

// gl11Forwarding runs the forwarding part of G-L11 through one member and
// returns its run ID: show, stat, PATCH set and unset of both instances,
// and the filtered list when list is true.
func (l *lab) gl11Forwarding(t *testing.T, cluster, member, stamp string, n int, uuids map[string]string, list bool) string {
	t.Helper()
	runID := stamp + "-g-l11-" + cluster + "-" + member
	s := session{evidenceDir: l.evidence, runID: runID}
	fixtures := filepath.Join(l.evidence, runID, "fixtures")
	const key = "user.lxd-probe.l11"
	t.Logf("through %s:", member)
	for _, name := range l11Instances {
		tags := []string{"--gate", "L11", "--cell", cluster + "-" + member + "-" + name, "--record", fixtures}
		probe := func(identity string, args ...string) *run {
			r := l.probeMember(t, s, cluster, member, identity, append(args, tags...)...)
			if r.rec.Server == nil || r.rec.Server.Member != member {
				t.Errorf("%s: the record names member %+v, not %s", strings.Join(args, " "), r.rec.Server, member)
			}
			return r
		}
		show := probe("full", "server", "instance", "show", name)
		stat := probe("files", "server", "file", "stat", name, "/etc/hostname")
		set := probe("edit", "server", "config", "set", name, key, fmt.Sprintf("v%d", n), "--method", "patch", "--if-match", "auto", "--allow-write", "--lab")
		unset := probe("edit", "server", "config", "unset", name, key, "--method", "patch-null", "--allow-write", "--lab")
		t.Logf("  %-9s show %s, location %s, %s; stat %s; set %s; unset %s", name,
			obs(show, "instance.http_status"), obs(show, "instance.location"), clusterGroup(show),
			obs(stat, "file.http_status"), l14View(set), l14View(unset))
		if list {
			r := probe("full", "server", "instance", "list", "--filter", "config.volatile.uuid eq "+uuids[name])
			t.Logf("  %-9s filtered list: %s %s", name, obs(r, "list.http_status"), obs(r, "instances"))
		}
	}
	if left := l.ssh(t, member, "sudo lxc config show c-unpriv; sudo lxc config show v-ubuntu"); strings.Contains(left, key) {
		t.Errorf("%s left behind", key)
	}
	return runID
}

// gl11Moves runs the move and evacuation part of G-L11 through the first
// member and returns its run ID.
func (l *lab) gl11Moves(t *testing.T, cluster string, members []string, stamp string, watchFor, readEvery time.Duration) string {
	t.Helper()
	first, second, third := members[0], members[1], members[2]
	runID := stamp + "-g-l11-" + cluster + "-moves"
	s := session{evidenceDir: l.evidence, runID: runID}
	tags := func(name string) []string { return []string{"--gate", "L11", "--cell", cluster + "-moves-" + name} }
	l.util(t, s, append([]string{"note", "live migration is not run: it needs migration.stateful for VMs and CRIU for containers"}, tags("notes")...)...)
	l.util(t, s, append([]string{"note", "snap and deb packages are not compared: Ubuntu 24.04 has no LXD deb; the lab runs the snap"}, tags("notes")...)...)

	started := time.Now()
	waits := map[string]func() *run{}
	for _, name := range l11Instances {
		waits[name] = l.startProbeMember(t, s, cluster, first, "full", append([]string{"server", "instance", "watch", name,
			"--for", watchFor.String(), "--interval", "1s"}, tags(name)...)...)
	}
	read := func(name string) string {
		show := l.probeMember(t, s, cluster, first, "full", append([]string{"server", "instance", "show", name}, tags(name)...)...)
		stat := l.probeMember(t, s, cluster, first, "files", append([]string{"server", "file", "stat", name, "/etc/hostname"}, tags(name)...)...)
		return fmt.Sprintf("show %s %s %s %s; stat %s %s", obs(show, "instance.http_status"), obs(show, "instance.status"),
			obs(show, "instance.location"), obs(show, "instance.error"), obs(stat, "file.http_status"), obs(stat, "file.error"))
	}
	// inflight runs an lxc command on the first member and reads name
	// every readEvery while it runs, then once more or, with answer,
	// until a file stat succeeds again (at most 5 minutes after the
	// command), logging each read.
	inflight := func(what, name, command string, answer bool) {
		t.Logf("%s (%s):", what, name)
		done := make(chan error, 1)
		go func() {
			_, err := l.trySSH(first, "sudo timeout 20m lxc "+command)
			done <- err
		}()
		var ended time.Time
		for {
			at := time.Now().UTC().Format("15:04:05")
			line := read(name)
			t.Logf("  %s %s", at, line)
			if ended.IsZero() {
				select {
				case err := <-done:
					ended = time.Now()
					if err != nil {
						t.Errorf("lxc %s: %v", command, err)
						return
					}
					t.Logf("  %s lxc %s: done", time.Now().UTC().Format("15:04:05"), command)
				default:
				}
			} else if !answer || strings.Contains(line, "; stat 200 ") || time.Since(ended) > 5*time.Minute {
				return
			}
			time.Sleep(readEvery)
		}
	}
	inflight("stop", "c-unpriv", "stop c-unpriv", false)
	inflight("move to "+third, "c-unpriv", "move c-unpriv --target "+third, false)
	inflight("start", "c-unpriv", "start c-unpriv", true)
	// 5.21 skips the confirmation with --force; 6.x with --yes, its
	// --force allowing the loss of Raft quorum instead
	confirm := "--yes"
	if strings.HasPrefix(l.env.Servers[cluster].Version, "5.") {
		confirm = "--force"
	}
	inflight("evacuate "+second, "v-ubuntu", "cluster evacuate "+second+" "+confirm, true)
	inflight("restore "+second, "v-ubuntu", "cluster restore "+second+" --force", true)
	for _, name := range l11Instances {
		t.Logf("after: %-9s %s", name, read(name))
	}
	if time.Since(started) > watchFor {
		t.Errorf("the operations took %s, longer than the watch (%s): raise LAB_L11_WATCH", time.Since(started).Round(time.Second), watchFor)
	}
	t.Logf("waiting for the watches (%s from %s)", watchFor, started.UTC().Format("15:04:05"))
	for _, name := range l11Instances {
		w := waits[name]()
		if w.rec == nil {
			continue
		}
		t.Logf("watch %-9s polls %s, poll failures %s", name, obs(w, "watch.polls"), obs(w, "watch.poll_failures"))
		changes, _ := w.observation("watch.changes")
		list, _ := changes.([]any)
		for _, c := range list {
			m, _ := c.(map[string]any)
			if m["field"] == "etag" {
				continue
			}
			t.Logf("  %v %v: %v -> %v", m["at"], m["field"], m["from"], m["to"])
		}
		t.Logf("  final %s", obs(w, "watch.final"))
	}
	return runID
}

// obs renders an observation compactly, or "-" when it is absent.
func obs(r *run, key string) string {
	v, ok := r.observation(key)
	if !ok {
		return "-"
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// clusterGroup renders the instance's volatile.cluster.group (6.x).
func clusterGroup(show *run) string {
	v, _ := show.observation("instance.config")
	config, _ := v.(map[string]any)
	if g, ok := config["volatile.cluster.group"]; ok {
		return fmt.Sprintf("volatile.cluster.group %v", g)
	}
	return "no volatile.cluster.group"
}

//go:build lab

package acceptance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// ruleL2 is gate L2's decision rule (probe spec, Gate runbooks).
const ruleL2 = "L2 decision: the propagation delay p95 sets the floor of the default proof_timeout; absence of " +
	"events makes polling normative in config_push; a restart caused by the write rejects config_push as a mode."

// clockSample is one reading of a remote clock: the remote time and the
// local times around it.
type clockSample struct{ Before, Remote, After float64 }

// offset estimates remote minus local from the sample with the shortest
// round trip, and its error bound (half the round trip).
func offset(samples []clockSample) (float64, float64) {
	best := slices.MinFunc(samples, func(a, b clockSample) int {
		return int(math.Copysign(1, (a.After-a.Before)-(b.After-b.Before)))
	})
	return best.Remote - (best.Before+best.After)/2, (best.After - best.Before) / 2
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// clockOffset estimates the guest's clock minus the lab host's through one
// open session (lab.sh ssh, then lxc exec of a shell that prints its clock
// at each line it reads), whose round trip is short; ten readings, the
// shortest round trip kept, half of it as error bound.
func (l *lab) clockOffset(t *testing.T, server, name string) map[string]float64 {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "ssh", server,
		"sudo lxc exec "+name+" -- sh -c 'while read l; do date +%s.%N; done'")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()    //nolint:errcheck // ends when stdin closes
	defer stdin.Close() //nolint:errcheck
	lines := bufio.NewReader(stdout)
	var samples []clockSample
	for i := range 11 {
		before := now()
		if _, err := io.WriteString(stdin, "\n"); err != nil {
			t.Fatal(err)
		}
		line, err := lines.ReadString('\n')
		after := now()
		if err != nil {
			t.Fatalf("clock of %s/%s: %v", server, name, err)
		}
		remote, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			t.Fatalf("clock of %s/%s: %q", server, name, line)
		}
		if i > 0 { // the first reading pays the session's start
			samples = append(samples, clockSample{before, remote, after})
		}
	}
	off, bound := offset(samples)
	return map[string]float64{"offset_s": off, "bound_s": bound, "samples": float64(len(samples))}
}

// startGuest starts a lab.sh guest command and returns a function that
// waits for it and returns its run.
func (l *lab) startGuest(t *testing.T, s session, server, name string, args ...string) func() *run {
	t.Helper()
	full := append([]string{"guest", server, name, "--"}, append(args, "--json")...)
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), full...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() *run {
		t.Helper()
		err := cmd.Wait()
		r := &run{stdout: stdout.String(), stderr: stderr.String()}
		if ee, ok := err.(*exec.ExitError); ok {
			r.code = ee.ExitCode()
		}
		dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		for dec.More() {
			var rec evidence.Record
			if dec.Decode(&rec) != nil || rec.ID == "" {
				break
			}
			r.recs = append(r.recs, &rec)
		}
		if len(r.recs) > 0 {
			r.rec = r.recs[0]
		}
		if probeFailure(r.code) || r.rec == nil {
			t.Fatalf("lab.sh %s: the probe failed: %s", strings.Join(full, " "), r)
		}
		return r
	}
}

// TestGL2Propagation is G-L2, gate L2's session: in a container and a VM
// of each server, guest devlxd watch of a key for 25 s while the server, as
// edit, writes it five times about 2 s apart. Each write's delay is the
// guest's first sight of the value minus the write's completion, corrected
// by the estimated clock offset; the session prints p50 and p95, whether
// events arrived, and the offset's error bound. It fails only when the
// probe fails or the evidence is not sanitized, and with LAB_COLLECT=yes
// copies each run, with its lab/l2.json, to docs/validation/.
func TestGL2Propagation(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	const key = "user.lxd-probe.l2"
	for _, server := range l.servers() {
		for _, name := range []string{"c-unpriv", "v-ubuntu"} {
			t.Run(server+"/"+name, func(t *testing.T) {
				runID := stamp + "-g-l2-" + server + "-" + name
				s := session{evidenceDir: l.evidence, runID: runID}
				tags := []string{"--gate", "L2", "--cell", server + "-" + name}
				clock := l.clockOffset(t, server, name)

				wait := l.startGuest(t, s, server, name, append([]string{"guest", "devlxd", "watch", key, "--for", "25s"}, tags...)...)
				time.Sleep(4 * time.Second) // the watch is reading the initial value
				completed := map[string]float64{}
				for i := 1; i <= 5; i++ {
					v := fmt.Sprintf("v%d", i)
					w := l.probe(t, s, server, "edit", append([]string{"server", "config", "set", name, key, v, "--wait", "--allow-write", "--lab"}, tags...)...)
					at, _ := w.observation("write.completed_at")
					if ts, err := time.Parse(time.RFC3339Nano, fmt.Sprint(at)); err == nil {
						completed[v] = float64(ts.UnixNano()) / 1e9
					}
					time.Sleep(2 * time.Second)
				}
				watch := wait()
				l.probe(t, s, server, "edit", append([]string{"server", "config", "unset", name, key, "--method", "patch-null", "--allow-write", "--lab"}, tags...)...)

				// each write's delay: its value's first sighting, by either source
				var changes []struct {
					At, Source, Value string
				}
				v, _ := watch.observation("watch.changes")
				data, _ := json.Marshal(v)
				_ = json.Unmarshal(data, &changes)
				delays := map[string]float64{}
				events := false
				for _, c := range changes {
					events = events || c.Source == "event"
					done, ok := completed[c.Value]
					if _, seen := delays[c.Value]; !ok || seen {
						continue
					}
					at, err := time.Parse(time.RFC3339Nano, c.At)
					if err != nil {
						continue
					}
					delays[c.Value] = (float64(at.UnixNano())/1e9 - clock["offset_s"] - done) * 1000
				}
				var ms []float64
				for _, d := range delays {
					ms = append(ms, d)
				}
				slices.Sort(ms)
				pct := func(p float64) float64 {
					if len(ms) == 0 {
						return math.NaN()
					}
					return ms[int(math.Ceil(p*float64(len(ms))))-1]
				}
				result := map[string]any{"clock": clock, "delays_ms": delays, "p50_ms": pct(0.5), "p95_ms": pct(0.95),
					"writes": len(completed), "seen": len(delays), "events": events}
				l.pull(t, server, "default", name, runID)
				labDir := filepath.Join(l.evidence, runID, "lab")
				if err := os.MkdirAll(labDir, 0o755); err != nil {
					t.Fatal(err)
				}
				out, _ := json.MarshalIndent(result, "", "  ")
				if err := os.WriteFile(filepath.Join(labDir, "l2.json"), append(out, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}

				runDir := filepath.Join(l.evidence, runID)
				staging := filepath.Join(t.TempDir(), runID)
				l.sanitize(t, runDir, staging)
				l.sanitizeCheck(t, staging)
				t.Logf("run %s: %d/%d writes seen, events %v, p50 %.0f ms, p95 %.0f ms (clock offset %.1f ms ± %.1f ms)",
					runID, len(delays), len(completed), events, pct(0.5), pct(0.95), clock["offset_s"]*1000, clock["bound_s"]*1000)
				t.Log(ruleL2)
				if collect && !t.Failed() {
					l.collectRun(t, runID, staging)
				}
			})
		}
	}
}

// TestGL1Step3IdmapCheck is gate L1's offline step: util idmap check on
// each committed G-L1 run's instance show and preflight records, whose
// records join that run as its util environment (with LAB_COLLECT=yes,
// in docs/validation/).
func TestGL1Step3IdmapCheck(t *testing.T) {
	l := newLab(t)
	collect := os.Getenv("LAB_COLLECT") == "yes"
	runs, _ := filepath.Glob(filepath.Join(l.repo, "docs", "validation", "*-g-l1-*"))
	if len(runs) == 0 {
		t.Fatal("no committed G-L1 runs")
	}
	for _, committed := range runs {
		runID := filepath.Base(committed)
		t.Run(runID, func(t *testing.T) {
			instances, _ := filepath.Glob(filepath.Join(committed, "server", "records", "*-instance-show.json"))
			preflight := filepath.Join(committed, "guest", "records", "0001-preflight.json")
			if len(instances) == 0 {
				t.Fatalf("no instance show record in %s", committed)
			}
			_, cell, _ := strings.Cut(runID, "-g-l1-")
			// a fresh util environment in the working run
			if err := os.RemoveAll(filepath.Join(l.evidence, runID, "util")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(filepath.Join(l.state, "bin", "lxd-probe"), "util", "idmap", "check", instances[0], preflight,
				"--evidence-dir", l.evidence, "--run-id", runID, "--gate", "L1", "--cell", cell, "--json")
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				t.Fatalf("util idmap check: %v\n%s", err, errOut.String())
			}
			var rec evidence.Record
			if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
				t.Fatalf("record: %v", err)
			}
			result, detail := "", ""
			for _, o := range rec.Observations {
				switch o.Key {
				case "idmap.check":
					result = fmt.Sprint(o.Value)
				case "idmap.detail":
					detail = fmt.Sprint(o.Value)
				}
			}
			t.Logf("%-42s %s %s", runID, result, detail)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, filepath.Join(l.evidence, runID), staging)
			l.sanitizeCheck(t, staging)
			if collect && !t.Failed() {
				dst := filepath.Join(committed, "util")
				if err := os.RemoveAll(dst); err != nil {
					t.Fatal(err)
				}
				if err := copyTree(filepath.Join(staging, "util"), dst, nil); err != nil {
					t.Fatal(err)
				}
				l.sanitizeCheck(t, dst)
			}
		})
	}
}

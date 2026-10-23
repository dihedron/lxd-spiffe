//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The end-to-end tests of the plugins scenario (lab test environment spec,
// E2E-1..5): real guests with SPIRE Agent and lxd-agent-plugin attest to a
// real SPIRE Server with lxd-server-plugin. Each test boots its guests,
// checks, and deletes them; none runs without the plugins scenario
// (lab.sh test --scenario plugins).

const spireServerBin = "sudo " + spirePrefix + "/bin/spire-server"

// Waits for what a guest needs: the packages, then the agent.
const (
	containerBoot = 6 * time.Minute
	vmBoot        = 10 * time.Minute
)

// e2eResult is one observation of an end-to-end test, kept in its summary.
type e2eResult struct {
	Test      string   `json:"test"`
	Server    string   `json:"server"`
	Guest     string   `json:"guest"`
	Kind      string   `json:"kind,omitempty"`
	Mode      string   `json:"proof_mode,omitempty"`
	SpiffeID  string   `json:"spiffe_id,omitempty"`
	Selectors []string `json:"selectors,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
	Seconds   float64  `json:"seconds,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

// e2eRun collects the results of one test, safely: servers run in parallel.
type e2eRun struct {
	mu      sync.Mutex
	results []e2eResult
}

func (r *e2eRun) add(res e2eResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

// finish writes the run's summary (and collects it with LAB_COLLECT=yes)
// when the test ends.
func (r *e2eRun) finish(t *testing.T, l *lab, name string) {
	t.Helper()
	p := l.plugins(t)
	servers := make([]string, 0, len(l.env.Servers))
	for s, v := range l.env.Servers {
		servers = append(servers, s+" LXD "+v.Version)
	}
	slices.Sort(servers)
	slices.SortFunc(r.results, func(a, b e2eResult) int {
		return strings.Compare(a.Server+a.Guest+a.Test, b.Server+b.Guest+b.Test)
	})
	header := fmt.Sprintf("SPIRE %s, plugins %s, %s", p.Spire.Version, p.Plugins.Version, strings.Join(servers, ", "))
	md := func(w io.Writer) error {
		fmt.Fprintf(w, "# %s\n\n%s\n\n| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |\n|---|---|---|---|---|---|---|---|\n", name, header) //nolint:errcheck // a buffer
		for _, x := range r.results {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %.0f | %s | %s |\n", x.Server, x.Guest, x.Kind, x.Mode, strings.Join(x.Reasons, ", "), x.Seconds, x.SpiffeID, strings.ReplaceAll(x.Notes, "\n", " ")) //nolint:errcheck // a buffer
		}
		return nil
	}
	js := func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"name": name, "environment": header, "results": r.results})
	}
	jsonl := func(w io.Writer) error {
		for _, x := range r.results {
			if err := json.NewEncoder(w).Encode(x); err != nil {
				return err
			}
		}
		return nil
	}
	l.writeRun(t, name, true, md, js, jsonl)
}

// spireAgent is an entry of spire-server agent list.
type spireAgent struct {
	ID struct {
		TrustDomain string `json:"trust_domain"`
		Path        string `json:"path"`
	} `json:"id"`
	Selectors []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"selectors"`
	Serial      string `json:"x509svid_serial_number"`
	Expires     string `json:"x509svid_expires_at"`
	CanReattest bool   `json:"can_reattest"`
	Banned      bool   `json:"banned"`
}

func (a spireAgent) spiffeID() string { return "spiffe://" + a.ID.TrustDomain + a.ID.Path }

func (a spireAgent) selectorValues() []string {
	var out []string
	for _, s := range a.Selectors {
		out = append(out, s.Value)
	}
	slices.Sort(out)
	return out
}

func (l *lab) spireAgents(t *testing.T) []spireAgent {
	t.Helper()
	var list struct {
		Agents []spireAgent `json:"agents"`
	}
	if err := json.Unmarshal([]byte(l.ssh(t, "spire", spireServerBin+" agent list -output json")), &list); err != nil {
		t.Fatalf("agent list: %v", err)
	}
	return list.Agents
}

// agentByID returns the attested agent with this SPIFFE ID.
func (l *lab) agentByID(t *testing.T, id string) (spireAgent, bool) {
	t.Helper()
	for _, a := range l.spireAgents(t) {
		if a.spiffeID() == id {
			return a, true
		}
	}
	return spireAgent{}, false
}

// logMark is the number of lines of SPIRE Server's journal so far; logSince
// returns the lines written after it.
func (l *lab) logMark(t *testing.T) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(l.ssh(t, "spire", "sudo journalctl -u spire-server --no-pager -o cat | wc -l")), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (l *lab) logSince(t *testing.T, mark int) string {
	t.Helper()
	return l.ssh(t, "spire", fmt.Sprintf("sudo journalctl -u spire-server --no-pager -o cat | tail -n +%d", mark+1))
}

var reasonRE = regexp.MustCompile(`reason_code=([a-z_]+)`)

// refusals returns the reason codes of the refusals logged after mark.
func (l *lab) refusals(t *testing.T, mark int) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(l.logSince(t, mark), "\n") {
		if m := reasonRE.FindStringSubmatch(line); m != nil && strings.Contains(line, "attestation refused") && !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// proofMode returns the proof mode the server logged for the success of the
// instance with this uuid.
func (l *lab) proofMode(t *testing.T, uuid string) string {
	t.Helper()
	re := regexp.MustCompile(`proof_mode=([a-z_]+)`)
	for _, line := range strings.Split(l.ssh(t, "spire", "sudo journalctl -u spire-server --no-pager -o cat | grep 'attestation succeeded' | grep "+uuid+"; true"), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// waitFor fails the test unless check holds within d (poll).
func waitFor(t *testing.T, d time.Duration, what string, check func() bool) {
	t.Helper()
	if !poll(d, check) {
		t.Fatalf("timed out after %v waiting for %s", d, what)
	}
}

// spireMode sets the server plugin's proof_mode for the test, and puts the
// lab's default back afterwards.
func (l *lab) spireMode(t *testing.T, mode string) {
	t.Helper()
	l.labsh(t, "spire-mode", mode)
	t.Cleanup(func() {
		if out, err := exec.Command(filepath.Join(l.dir, "lab.sh"), "spire-mode", "default").CombinedOutput(); err != nil {
			t.Errorf("restoring proof_mode: %v\n%s", err, out)
		}
	})
}

// guest is a guest booted by a test.
type guest struct {
	server, name, project, kind string
	start                       time.Time
}

// guestUp launches a guest and deletes it when the test ends.
func (l *lab) guestUp(t *testing.T, server, name string, flags ...string) *guest {
	t.Helper()
	g := &guest{server: server, name: name, project: "default", kind: "container", start: time.Now()}
	for i, f := range flags {
		switch f {
		case "--vm":
			g.kind = "vm"
		case "--privileged":
			g.kind = "privileged container"
		case "--project":
			g.project = flags[i+1]
		}
	}
	l.labsh(t, append([]string{"guest-up", server, name}, flags...)...)
	t.Cleanup(func() { l.guestDown(t, g) })
	return g
}

func (l *lab) guestDown(t *testing.T, g *guest) {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), "guest-down", g.server, g.name, "--project", g.project)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("guest-down %s: %v\n%s", g.name, err, out)
	}
}

func (l *lab) uuid(t *testing.T, g *guest) string {
	t.Helper()
	return strings.TrimSpace(l.ssh(t, g.server, fmt.Sprintf("sudo lxc config get %s volatile.uuid --project %s", g.name, g.project)))
}

func (g *guest) timeout() time.Duration {
	if g.kind == "vm" {
		return vmBoot
	}
	return containerBoot
}

// spiffeID is the SPIFFE ID the attestor gives the guest (FR-S7).
func (l *lab) spiffeID(g *guest, uuid string) string {
	return fmt.Sprintf("spiffe://%s/spire/agent/lxd_instance/%s/%s", labTrustDomain, g.server, uuid)
}

const labTrustDomain = "lxd.lab"

// attested waits for the guest's agent to be attested and checks its
// selectors and the proof mode the server logged (FR-S7, FR-S8).
func (l *lab) attested(t *testing.T, g *guest, mode string, run *e2eRun, test string) spireAgent {
	t.Helper()
	uuid := l.uuid(t, g)
	id := l.spiffeID(g, uuid)
	var agent spireAgent
	waitFor(t, g.timeout(), "the agent of "+g.name+" to attest as "+id, func() bool {
		var ok bool
		agent, ok = l.agentByID(t, id)
		return ok
	})
	typ := "container"
	if g.kind == "vm" {
		typ = "virtual-machine"
	}
	want := []string{"server:" + g.server, "uuid:" + uuid, "project:" + g.project, "type:" + typ}
	got := agent.selectorValues()
	// privileged is a container's selector: a VM has none
	if g.kind == "vm" {
		if slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "privileged:") }) {
			t.Errorf("%s: a VM has a privileged selector in %v", g.name, got)
		}
	} else {
		want = append(want, "privileged:"+fmt.Sprint(g.kind == "privileged container"))
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("%s: selector %q missing from %v", g.name, w, got)
		}
	}
	for _, prefix := range []string{"profile:", "network:"} {
		if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, prefix) }) {
			t.Errorf("%s: no %s selector in %v", g.name, prefix, got)
		}
	}
	if !agent.CanReattest || agent.Banned {
		t.Errorf("%s: can_reattest %v, banned %v", g.name, agent.CanReattest, agent.Banned)
	}
	used := l.proofMode(t, uuid)
	if mode != "" && used != mode {
		t.Errorf("%s attested with %q, want %q", g.name, used, mode)
	}
	run.add(e2eResult{Test: test, Server: g.server, Guest: g.name, Kind: g.kind, Mode: used, SpiffeID: id, Selectors: got, Seconds: time.Since(g.start).Seconds()})
	return agent
}

// challengeKeys counts the challenge keys left on an instance.
func (l *lab) challengeKeys(t *testing.T, g *guest) int {
	t.Helper()
	out, _ := l.trySSH(g.server, fmt.Sprintf("sudo lxc config show %s --project %s | grep -c user.spire.challenge; true", g.name, g.project))
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(out), &n); err != nil {
		t.Fatalf("counting challenge keys: %q", out)
	}
	return n
}

// attestAll boots an unprivileged container, a privileged container and a
// VM on every standalone server under the proof mode, and checks that each
// attests (E2E-1, E2E-2).
func attestAll(t *testing.T, test, mode string) {
	l := requirePlugins(t)
	l.spireMode(t, mode)
	run := &e2eRun{}
	t.Cleanup(func() {
		run.finish(t, l, fmt.Sprintf("%s-%s-summary", time.Now().UTC().Format("20060102T150405Z"), strings.ToLower(test)))
	})
	// the group returns when its parallel subtests have finished: the
	// caller may change the mode afterwards
	t.Run("servers", func(t *testing.T) {
		for _, server := range l.servers() {
			t.Run(server, func(t *testing.T) {
				t.Parallel()
				guests := []*guest{
					l.guestUp(t, server, "e2e-unpriv"),
					l.guestUp(t, server, "e2e-priv", "--privileged"),
					l.guestUp(t, server, "e2e-vm", "--vm"),
				}
				for _, g := range guests {
					l.attested(t, g, mode, run, test)
					if mode == "config_push" {
						if n := l.challengeKeys(t, g); n != 0 {
							t.Errorf("%s: %d challenge keys left after the attestation", g.name, n)
						}
					}
				}
			})
		}
	})
}

// TestE2E1FilePull: E2E-1.
func TestE2E1FilePull(t *testing.T) { attestAll(t, "E2E-1", "file_pull") }

// TestE2E2ConfigPush: E2E-2, and auto choosing file_pull.
func TestE2E2ConfigPush(t *testing.T) {
	attestAll(t, "E2E-2", "config_push")
	t.Run("auto", func(t *testing.T) {
		l := requirePlugins(t)
		l.spireMode(t, "auto")
		run := &e2eRun{}
		t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-2-auto-summary") })
		for _, server := range l.servers() {
			l.attested(t, l.guestUp(t, server, "e2e-auto"), "file_pull", run, "E2E-2 auto")
		}
	})
}

// TestE2E3Refusals: E2E-3. The agent only ever sees the uniform error; the
// server's log has the reason, and the rate limit may add rate_limited to
// the retries of the same claim.
func TestE2E3Refusals(t *testing.T) {
	l := requirePlugins(t)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-3-summary") })
	// A claim naming another instance carries the guest's own cloud-init
	// instance id, which the other instance does not have (FR-S3): the
	// server finds no match, whether its identity could read that instance
	// (granted for this case) or not. proof_mismatch needs an agent that
	// forges the id: a unit test, not this lab's stock agent.
	cases := []struct {
		name, reason string
		flags        []string
		visible      bool // lab-spire is granted access to c-unpriv for the case
	}{
		{"outside the allowed projects", "scope_denied", []string{"--project", "restricted"}, false},
		{"a claim naming another instance", "instance_not_found", []string{"--claim-name", "c-unpriv"}, true},
		{"an identity lacking can_access_files", "lxd_unavailable", []string{"--no-files"}, false},
	}
	for _, server := range l.servers() {
		for i, c := range cases {
			t.Run(server+"/"+c.name, func(t *testing.T) {
				if c.visible {
					l.grantSpire(t, server, "c-unpriv")
				}
				mark := l.logMark(t)
				g := l.guestUp(t, server, fmt.Sprintf("e2e-refused-%d", i), c.flags...)
				var reasons []string
				waitFor(t, containerBoot, "the server to refuse "+c.name, func() bool {
					reasons = l.refusals(t, mark)
					return slices.Contains(reasons, c.reason)
				})
				for _, r := range reasons {
					if r != c.reason && r != "rate_limited" {
						t.Errorf("reasons %v, want only %s (and rate_limited)", reasons, c.reason)
					}
				}
				agentLog := l.ssh(t, server, fmt.Sprintf("sudo lxc exec %s --project %s -- journalctl -u spire-agent --no-pager -o cat | tail -n 40", g.name, g.project))
				for _, r := range reasons {
					if strings.Contains(agentLog, r) {
						t.Errorf("SEC-10: the agent's log names the reason %s", r)
					}
				}
				for _, a := range l.spireAgents(t) {
					if slices.Contains(a.selectorValues(), "server:"+server) && l.instanceNamed(t, a, g) {
						t.Errorf("a refused guest is attested as %s", a.spiffeID())
					}
				}
				run.add(e2eResult{Test: "E2E-3", Server: server, Guest: c.name, Kind: "container", Reasons: reasons, Notes: agentError(agentLog)})
			})
		}
	}
}

// grantSpire gives lab-spire the entitlements of a SPIRE identity that sees
// every instance of the project on an existing instance of default, until
// the test ends.
func (l *lab) grantSpire(t *testing.T, server, instance string) {
	t.Helper()
	for _, e := range []string{"can_view", "can_access_files"} {
		l.ssh(t, server, fmt.Sprintf("sudo lxc auth group permission add lab-spire instance %s %s project=default", instance, e))
		t.Cleanup(func() {
			if _, err := l.trySSH(server, fmt.Sprintf("sudo lxc auth group permission remove lab-spire instance %s %s project=default", instance, e)); err != nil {
				t.Errorf("removing lab-spire's %s on %s: %v", e, instance, err)
			}
		})
	}
}

// agentError is the last error line of a SPIRE Agent's log, or its last line.
func agentError(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "level=error") {
			return lines[i]
		}
	}
	return lastLine(log)
}

// instanceNamed reports whether the attested agent is the guest's.
func (l *lab) instanceNamed(t *testing.T, a spireAgent, g *guest) bool {
	t.Helper()
	uuid := strings.TrimSpace(l.ssh(t, g.server, fmt.Sprintf("sudo lxc config get %s volatile.uuid --project %s", g.name, g.project)))
	return slices.Contains(a.selectorValues(), "uuid:"+uuid)
}

// TestE2E4Reattestation: E2E-4. An agent whose data is wiped attests again
// as the same instance; a copy that attests is another instance; what a
// copy does that inherits the agent's data is recorded, not asserted.
func TestE2E4Reattestation(t *testing.T) {
	l := requirePlugins(t)
	l.spireMode(t, "file_pull")
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-4-summary") })
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			t.Parallel()
			g := l.guestUp(t, server, "e2e-re")
			first := l.attested(t, g, "file_pull", run, "E2E-4 first attestation")

			l.ssh(t, server, "sudo lxc exec e2e-re -- sh -c 'systemctl stop spire-agent && rm -rf /var/lib/spire/agent/* && systemctl start spire-agent'")
			waitFor(t, 3*time.Minute, "the agent with wiped data to attest again", func() bool {
				a, ok := l.agentByID(t, first.spiffeID())
				return ok && a.Serial != first.Serial
			})
			run.add(e2eResult{Test: "E2E-4 wiped agent", Server: server, Guest: g.name, SpiffeID: first.spiffeID(), Notes: "attested again, new serial"})

			l.labsh(t, "guest-copy", server, "e2e-re", "e2e-re-copy")
			copyG := &guest{server: server, name: "e2e-re-copy", project: "default", kind: "container", start: time.Now()}
			t.Cleanup(func() { l.guestDown(t, copyG) })
			copied := l.attested(t, copyG, "file_pull", run, "E2E-4 copy that attests")
			if copied.spiffeID() == first.spiffeID() {
				t.Errorf("the copy has the original's SPIFFE ID %s", copied.spiffeID())
			}

			before, _ := l.agentByID(t, first.spiffeID())
			l.labsh(t, "guest-copy", server, "e2e-re", "e2e-re-kept", "--keep-agent")
			keptG := &guest{server: server, name: "e2e-re-kept", project: "default", kind: "container", start: time.Now()}
			t.Cleanup(func() { l.guestDown(t, keptG) })
			l.recordKeptCopy(t, run, keptG, first, before)
		})
	}
}

// recordKeptCopy records what a copy does that inherited its original's
// agent data: whether it attests as itself, which SVID its agent loads,
// whether the original's SVID moved, and whether workloads in the copy get
// the original's workload SVIDs (a registration entry under the original's
// agent ID, fetched from inside the copy).
func (l *lab) recordKeptCopy(t *testing.T, run *e2eRun, g *guest, original, before spireAgent) {
	t.Helper()
	waitFor(t, containerBoot, "the copy's SPIRE Agent to run", func() bool {
		out, _ := l.trySSH(g.server, "sudo lxc exec "+g.name+" -- systemctl is-active spire-agent")
		return strings.TrimSpace(out) == "active"
	})
	time.Sleep(90 * time.Second) // time to attest, or to show that it does not
	uuid := l.uuid(t, g)
	_, attestsAsItself := l.agentByID(t, l.spiffeID(g, uuid))
	after, _ := l.agentByID(t, original.spiffeID())
	log := l.ssh(t, g.server, "sudo lxc exec "+g.name+" -- sh -c \"journalctl -u spire-agent -b --no-pager -o cat | grep -E 'SVID loaded|Node attestation' | tail -n 4\"")

	// a workload entry under the original's agent: does the copy get it?
	spiffe := "spiffe://" + labTrustDomain + "/e2e-clone"
	out := l.ssh(t, "spire", fmt.Sprintf("%s entry create -parentID %s -spiffeID %s -selector unix:uid:0 -output json", spireServerBin, original.spiffeID(), spiffe))
	var created struct {
		Results []struct {
			Entry struct {
				ID string `json:"id"`
			} `json:"entry"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || len(created.Results) != 1 {
		t.Fatalf("entry create: %v %s", err, out)
	}
	t.Cleanup(func() { l.trySSH("spire", spireServerBin+" entry delete -entryID "+created.Results[0].Entry.ID) }) //nolint:errcheck // best effort
	fetched := poll(2*time.Minute, func() bool {
		out, _ := l.trySSH(g.server, "sudo lxc exec "+g.name+" -- "+spirePrefix+"/bin/spire-agent api fetch x509 -socketPath /run/spire/agent/public/api.sock")
		return strings.Contains(out, spiffe)
	})
	notes := fmt.Sprintf("copy attests as itself: %v; the copy's workload gets the original's workload SVID %s: %v; original's agent serial before %s, after %s; the copy's agent log: %s",
		attestsAsItself, spiffe, fetched, before.Serial, after.Serial, strings.ReplaceAll(strings.TrimSpace(log), "\n", " / "))
	run.add(e2eResult{Test: "E2E-4 copy with the agent's data (recorded, not asserted)", Server: g.server, Guest: g.name, SpiffeID: original.spiffeID(), Notes: notes})
	t.Logf("copy with the agent's data: %s", notes)
}

// TestE2E5Cluster: E2E-5. A guest placed on a cluster's second member
// attests through the server entry, which is the first member's address,
// with each proof mode.
func TestE2E5Cluster(t *testing.T) {
	l := requirePlugins(t)
	clusters := l.clusters()
	if len(clusters) == 0 {
		t.Skip("no cluster is running (LAB_CLUSTER)")
	}
	run := &e2eRun{}
	t.Cleanup(func() { run.finish(t, l, time.Now().UTC().Format("20060102T150405Z")+"-e2e-5-summary") })
	for _, cluster := range clusters {
		member := l.members(cluster)[1]
		for _, mode := range []string{"file_pull", "config_push"} {
			t.Run(cluster+"/"+mode, func(t *testing.T) {
				l.spireMode(t, mode)
				g := l.guestUp(t, cluster, "e2e-"+strings.ReplaceAll(mode, "_", "-"), "--target", member)
				agent := l.attested(t, g, mode, run, "E2E-5")
				if where := strings.TrimSpace(l.ssh(t, cluster, fmt.Sprintf("sudo lxc list %s --project default -f csv -c L", g.name))); where != member {
					t.Errorf("%s runs on %q, want %s", g.name, where, member)
				}
				if mode == "config_push" {
					if n := l.challengeKeys(t, g); n != 0 {
						t.Errorf("%d challenge keys left", n)
					}
				}
				if !strings.Contains(agent.spiffeID(), "/lxd_instance/"+cluster+"/") {
					t.Errorf("SPIFFE ID %s does not carry the server id %s", agent.spiffeID(), cluster)
				}
			})
		}
	}
}

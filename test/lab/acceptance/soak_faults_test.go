//go:build lab

package acceptance

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

// The faults of the real soak (lab stress spec, STR-52), injected one at a
// time at random moments from a seeded generator: the real agents attest
// on their own, so a fault cannot be timed to a step as in Phase A.

// soakEvent is one fault or one change of the soak's configuration.
type soakEvent struct {
	Fault     string    `json:"fault"`
	Target    string    `json:"target,omitempty"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Mode      string    `json:"proof_mode"`
	Note      string    `json:"note,omitempty"`
	Disturbed []string  `json:"disturbed,omitempty"`
}

// soakSample is a sample of the processes' memory (INV-6).
type soakSample struct {
	At        time.Time `json:"at"`
	ServerKiB int       `json:"spire_server_rss_kib"`
	PluginKiB int       `json:"server_plugin_rss_kib"`
	LXDKiB    int       `json:"lxd_rss_kib"`
	Agents    int       `json:"agents"`
}

// soakRun is one soak: the lab, its settings and what it has done.
type soakRun struct {
	l     *lab
	s     soakSettings
	t     *testing.T
	names []string
	uuids map[string]string
	rng   *rand.Rand
	mode  string

	mu      sync.Mutex
	events  []soakEvent
	samples []soakSample
	daemons int
}

func newSoakRun(t *testing.T, l *lab, s soakSettings, uuids map[string]string) *soakRun {
	r := &soakRun{l: l, s: s, t: t, uuids: uuids, rng: rand.New(rand.NewPCG(s.seed, s.seed)), mode: "file_pull"}
	r.names = fleetNames(s.fleet)
	return r
}

// vm runs a command, bounded, on the LXD server's VM; spire on the spire VM.
func (r *soakRun) vm(d time.Duration, command string) (string, error) {
	return r.l.trySSH(r.s.server, boundedRemote(d, command))
}

func (r *soakRun) spire(d time.Duration, command string) (string, error) {
	return r.l.trySSH("spire", boundedRemote(d, command))
}

func (r *soakRun) record(e soakEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Mode = r.mode
	r.events = append(r.events, e)
	r.t.Logf("fault %s %s: %s - %s %s", e.Fault, e.Target, e.Start.Format("15:04:05"), e.End.Format("15:04:05"), e.Note)
}

func (r *soakRun) instance() string { return r.names[r.rng.IntN(len(r.names))] }

func (r *soakRun) pause(min, max int) {
	time.Sleep(time.Duration(min+r.rng.IntN(max-min+1)) * time.Second)
}

// soakFaults are the faults and their weights.
var soakFaults = []struct {
	id     string
	weight int
}{
	{"stop", 25}, {"wipe", 20}, {"partition", 10}, {"writer", 10}, {"spire-restart", 8}, {"plugin-kill", 7}, {"daemon", 3},
}

// pick draws a fault; the concurrent writer matters only to config_push, and
// an LXD crash restarts every container, so there are few and not at first.
func (r *soakRun) pick(sinceStart time.Duration) string {
	for {
		total := 0
		for _, f := range soakFaults {
			total += f.weight
		}
		n := r.rng.IntN(total)
		for _, f := range soakFaults {
			if n -= f.weight; n >= 0 {
				continue
			}
			switch {
			case f.id == "writer" && r.mode != "config_push":
			case f.id == "daemon" && (r.daemons >= 3 || sinceStart < 5*time.Minute):
			default:
				return f.id
			}
			break
		}
	}
}

// inject performs a fault and its clean-up.
func (r *soakRun) inject(id string) {
	start := time.Now().UTC()
	var e soakEvent
	switch id {
	case "stop":
		e = r.stopInstance()
	case "wipe":
		e = r.wipeAgent()
	case "partition":
		e = r.partition()
	case "writer":
		e = r.writer()
	case "spire-restart":
		e = r.restartSpire()
	case "plugin-kill":
		e = r.killPlugin()
	case "daemon":
		e = r.killDaemon()
	}
	e.Fault = id
	if e.Start.IsZero() {
		e.Start = start
	}
	e.End = time.Now().UTC()
	r.record(e)
}

// stopInstance stops a fleet instance for a while: no agent in it can
// attest meanwhile, and none must be attested for it (INV-1).
func (r *soakRun) stopInstance() soakEvent {
	n := r.instance()
	_, err := r.vm(90*time.Second, "lxc stop --force "+n)
	e := soakEvent{Target: n, Start: time.Now().UTC(), Disturbed: []string{n}}
	r.pause(15, 40)
	e.End = time.Now().UTC()
	if _, err2 := r.vm(90*time.Second, "lxc start "+n); err2 != nil || err != nil {
		e.Note = fmt.Sprintf("stop: %v, start: %v", err, err2)
	}
	return e
}

// wipeAgent stops an agent, removes its data and starts it: it must attest
// as a new node.
func (r *soakRun) wipeAgent() soakEvent {
	n := r.instance()
	_, err := r.vm(90*time.Second, fmt.Sprintf("lxc exec %s -- sh -c 'rc-service spire-agent stop; rm -rf /var/lib/spire/agent/*; rc-service spire-agent start'", n))
	e := soakEvent{Target: n, Disturbed: []string{n}}
	if err != nil {
		e.Note = err.Error()
	}
	return e
}

// partition cuts SPIRE Server off from LXD's port for 10 to 25 seconds, the
// revert armed on the VM before the cut (STR-61).
func (r *soakRun) partition() soakEvent {
	seconds := 10 + r.rng.IntN(16)
	ip := r.l.plugins(r.t).VMs["spire"].IP
	rule := fmt.Sprintf("INPUT -s %s -p tcp --dport 8443 -j DROP", ip)
	unit := fmt.Sprintf("lab-soak-%d", time.Now().UnixNano()%1000000)
	_, err := r.vm(30*time.Second, fmt.Sprintf("systemd-run --quiet --unit=%s --on-active=%ds iptables -D %s && iptables -I %s", unit, seconds, rule, rule))
	e := soakEvent{Start: time.Now().UTC(), Note: fmt.Sprintf("%d s", seconds)}
	if err != nil {
		e.Note += ": " + err.Error()
	}
	waitFor(r.t, time.Duration(seconds+40)*time.Second, "the partition to end", func() bool {
		_, err := r.l.trySSH(r.s.server, "sudo iptables -C "+rule+" 2>/dev/null")
		return err != nil
	})
	return e
}

// writer changes an instance's config in a loop for 20 seconds, so that the
// server's conditional writes meet 412s (FR-S14).
func (r *soakRun) writer() soakEvent {
	n := r.instance()
	script := fmt.Sprintf("end=$(( $(date +%%s) + 20 )); i=0; while [ $(date +%%s) -lt $end ]; do i=$((i+1)); lxc config set %[1]s user.soak.noise=$i; done; lxc config unset %[1]s user.soak.noise", n)
	_, err := r.vm(120*time.Second, script)
	e := soakEvent{Target: n, Disturbed: []string{n}}
	if err != nil {
		e.Note = err.Error()
	}
	return e
}

// restartSpire restarts SPIRE Server, and with it the plugin.
func (r *soakRun) restartSpire() soakEvent {
	_, err := r.spire(2*time.Minute, "systemctl restart spire-server")
	e := soakEvent{}
	if err != nil {
		e.Note = err.Error()
	}
	waitFor(r.t, 2*time.Minute, "SPIRE Server to be healthy", func() bool {
		_, err := r.spire(20*time.Second, spirePrefix+"/bin/spire-server healthcheck")
		return err == nil
	})
	return e
}

// killPlugin kills the server plugin's process. Does SPIRE restart it? If
// the plugin process is not back within 150 s, the finding is recorded and SPIRE
// Server restarted.
func (r *soakRun) killPlugin() soakEvent {
	_, _ = r.spire(30*time.Second, "pkill -9 -x lxd-server-plugin")
	killed := time.Now().UTC()
	e := soakEvent{Start: killed}
	if poll(150*time.Second, func() bool {
		_, err := r.spire(20*time.Second, "pgrep -x lxd-server-plugin")
		return err == nil
	}) {
		e.Note = "SPIRE restarted the plugin"
		return e
	}
	e.Note = "NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted"
	_, _ = r.spire(2*time.Minute, "systemctl restart spire-server")
	waitFor(r.t, 2*time.Minute, "SPIRE Server to be healthy", func() bool {
		_, err := r.spire(20*time.Second, spirePrefix+"/bin/spire-server healthcheck")
		return err == nil
	})
	return e
}

// killDaemon kills LXD's daemon: it restarts, and so may the containers;
// the fault ends when LXD answers and the whole fleet runs again.
func (r *soakRun) killDaemon() soakEvent {
	r.daemons++
	_, _ = r.vm(30*time.Second, "kill -9 $(pgrep -x lxd)")
	e := soakEvent{Start: time.Now().UTC()}
	waitFor(r.t, 3*time.Minute, "LXD to answer again", func() bool {
		_, err := r.vm(15*time.Second, "lxc info >/dev/null")
		return err == nil
	})
	var stopped string
	waitFor(r.t, 5*time.Minute, "the fleet to run again", func() bool {
		out, _ := r.vm(60*time.Second, `lxc list '^s-[0-9]{3}$' -c ns -f csv | grep -v ',RUNNING' | cut -d, -f1`)
		stopped = strings.Join(strings.Fields(out), " ")
		if stopped != "" {
			_, _ = r.vm(120*time.Second, "for n in "+stopped+"; do lxc start $n; done")
		}
		return stopped == ""
	})
	e.Disturbed = r.names
	e.Note = "the fleet's containers run again"
	return e
}

// switchMode changes the server plugin's proof mode, which restarts SPIRE
// Server.
func (r *soakRun) switchMode(mode string) {
	start := time.Now().UTC()
	r.l.labshEnv(r.t, []string{"LAB_SPIRE_AGENT_TTL=" + r.s.ttl.String()}, "spire-mode", mode)
	r.mode = mode
	r.record(soakEvent{Fault: "mode-switch", Start: start, End: time.Now().UTC(), Note: "proof_mode " + mode})
}

// sample records the processes' memory and the number of attested agents.
func (r *soakRun) sample() {
	kib := func(out string) int { return atoi(strings.Fields(out + " 0")[0]) }
	srv, _ := r.spire(20*time.Second, "ps -C spire-server -o rss= | head -n1")
	plg, _ := r.spire(20*time.Second, "ps -C lxd-server-plugin -o rss= | head -n1")
	lxd, _ := r.vm(20*time.Second, "ps -C lxd -o rss= | head -n1")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, soakSample{At: time.Now().UTC(), ServerKiB: kib(srv), PluginKiB: kib(plg), LXDKiB: kib(lxd),
		Agents: len(r.l.fleetAgents(r.t, r.s.server, r.uuids))})
}

// run drives the soak: faults at random moments until the final quiet
// period, the proof mode changing from file_pull to config_push halfway.
func (r *soakRun) run() {
	start := time.Now()
	quiet := r.s.ttl/2 + 150*time.Second
	faultsEnd := start.Add(r.s.duration - quiet)
	half := start.Add((r.s.duration - quiet) / 2)
	next := start.Add(3 * time.Minute)
	nextSample := start
	switched := false
	for time.Now().Before(faultsEnd) {
		switch now := time.Now(); {
		case now.After(nextSample):
			r.sample()
			nextSample = now.Add(time.Minute)
		case !switched && now.After(half):
			r.switchMode("config_push")
			switched = true
		case now.After(next):
			r.inject(r.pick(now.Sub(start)))
			next = time.Now().Add(time.Duration(float64(r.s.every) * (0.5 + r.rng.Float64())))
		default:
			time.Sleep(5 * time.Second)
		}
	}
	r.t.Logf("faults over after %s; %s of quiet", time.Since(start).Round(time.Second), quiet)
	for end := time.Now().Add(quiet); time.Now().Before(end); time.Sleep(time.Minute) {
		r.sample()
	}
}

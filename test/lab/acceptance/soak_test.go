//go:build lab

package acceptance

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The real soak (lab stress spec, Phase B): ST-7 builds the fleet of
// agents; the soak itself follows in this file.

// soakSettings are the soak's settings, from the environment.
type soakSettings struct {
	server   string        // LAB_SOAK_SERVER, a standalone server
	fleet    int           // LAB_SOAK_FLEET
	duration time.Duration // LAB_SOAK_DURATION, faults and the final quiet period
	every    time.Duration // LAB_SOAK_EVERY, the mean gap between faults
	ttl      time.Duration // LAB_SOAK_AGENT_TTL, the agents' SVID TTL
	// attest is LAB_SOAK_ATTEST_TIMEOUT, the agents' attestation_timeout:
	// SPIRE Server queues the attestations of agents sharing an address,
	// one per second, and an agent that leaves the queue is attested for
	// nobody (100 agents on the 30 s default left 59 unattested after 10
	// minutes)
	attest time.Duration
	seed   uint64 // LAB_SOAK_SEED
}

func loadSoakSettings(t *testing.T) soakSettings {
	t.Helper()
	duration := func(name, def string) time.Duration {
		d, err := time.ParseDuration(setting(name, def))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return d
	}
	s := soakSettings{
		server:   setting("LAB_SOAK_SERVER", "lxd6"),
		fleet:    atoi(setting("LAB_SOAK_FLEET", "100")),
		duration: duration("LAB_SOAK_DURATION", "45m"),
		every:    duration("LAB_SOAK_EVERY", "90s"),
		ttl:      duration("LAB_SOAK_AGENT_TTL", "5m"),
		attest:   duration("LAB_SOAK_ATTEST_TIMEOUT", "4m"),
		seed:     uint64(time.Now().UnixNano()),
	}
	if v := setting("LAB_SOAK_SEED", ""); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("LAB_SOAK_SEED: %v", err)
		}
		s.seed = seed
	}
	if s.fleet < 1 || s.fleet > 300 {
		t.Fatalf("LAB_SOAK_FLEET %d: 1 to 300", s.fleet)
	}
	return s
}

// baselineVMs are the baseline's virtual machines: the soak stops them,
// since the fleet needs their memory, and starts them again afterwards.
var baselineVMs = []string{"v-ubuntu", "v-debian", "v-alpine", "v-rocky", "v-alma"}

// soakSetup prepares SPIRE Server for the soak (a short agent TTL, so that
// agents attest continuously), frees the baseline VMs' memory, builds the
// fleet and waits until every agent is attested; everything is undone when
// the test ends.
func (l *lab) soakSetup(t *testing.T, s soakSettings) map[string]string {
	t.Helper()
	ttl := []string{"LAB_SPIRE_AGENT_TTL=" + s.ttl.String()}
	l.labshEnv(t, ttl, "spire-mode", "file_pull")
	t.Cleanup(func() { l.labshEnv(t, nil, "spire-mode", "default") })
	vms := strings.Join(baselineVMs, " ")
	l.trySSH(s.server, "for v in "+vms+"; do sudo timeout -k 5 120 lxc stop --force $v; done")                //nolint:errcheck // one may be absent
	t.Cleanup(func() { l.trySSH(s.server, "for v in "+vms+"; do sudo timeout -k 5 120 lxc start $v; done") }) //nolint:errcheck // best effort
	l.soakFleetUp(t, s.server, fleetNames(s.fleet), s.attest)
	uuids := l.fleetUUIDs(t, s.server)
	if len(uuids) != s.fleet {
		t.Fatalf("%d fleet instances, want %d", len(uuids), s.fleet)
	}
	start := time.Now()
	l.waitFleetAttested(t, s.server, uuids, 10*time.Minute)
	t.Logf("%d agents attested %s after their start", s.fleet, time.Since(start).Round(time.Second))
	return uuids
}

// vmMemory returns the used and available memory of a VM, in MiB.
func (l *lab) vmMemory(t *testing.T, vm string) (used, available int) {
	t.Helper()
	f := strings.Fields(l.ssh(t, vm, "free -m | sed -n 2p"))
	if len(f) < 7 {
		t.Fatalf("free: %v", f)
	}
	return atoi(f[2]), atoi(f[6])
}

// TestST7Fleet: the fleet of real agents builds and attests; what it costs
// in memory. A smoke of the soak: LAB_SOAK_FLEET sets its size.
func TestST7Fleet(t *testing.T) {
	l := requirePlugins(t)
	s := loadSoakSettings(t)
	beforeUsed, _ := l.vmMemory(t, s.server)
	uuids := l.soakSetup(t, s)
	used, available := l.vmMemory(t, s.server)
	t.Logf("%s memory: %d MiB used before the fleet, %d after (%d MiB per agent), %d MiB available", s.server, beforeUsed, used, (used-beforeUsed)/s.fleet, available)
	agents := l.fleetAgents(t, s.server, uuids)
	var first spireAgent
	for _, a := range agents {
		first = a
		break
	}
	t.Logf("an agent: %s, selectors %v", first.spiffeID(), first.selectorValues())
	if len(agents) != s.fleet {
		t.Errorf("%d agents attested, want %d", len(agents), s.fleet)
	}
}

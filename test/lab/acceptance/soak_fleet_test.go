//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fleet of the real soak (lab stress spec, Phase B, STR-50): Alpine
// containers, each with a SPIRE Agent and lxd-agent-plugin, built from one
// template image so that a hundred of them start quickly and fit the LXD
// VM's memory.

const (
	soakTemplate = "soak-template"
	soakImage    = "soak-agent"
	// soakShardCapacity is the most instance grants a lab-spire shard holds
	// (a group holds 99 permissions, gate L17; project can_view is one).
	soakShardCapacity = 90
)

// soakEntitlements are lab-spire's entitlements on each fleet instance:
// both proof modes are enabled.
var soakEntitlements = []string{"can_view", "can_access_files", "can_edit"}

// labshEnv runs lab.sh with extra environment variables and returns its
// standard output.
func (l *lab) labshEnv(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("lab.sh %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return string(out)
}

// soakShardScript gives the spire identity its grants on the fleet through
// shard groups lab-spire-s1, lab-spire-s2, …: each holds project can_view
// and at most soakShardCapacity instance grants, and the identity is added
// to it (the attestor's Least privilege, gate L17).
func soakShardScript(names []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	per := soakShardCapacity / len(soakEntitlements)
	for k := 0; k*per < len(names); k++ {
		shard := fmt.Sprintf("lab-spire-s%d", k+1)
		fmt.Fprintf(&b, "sudo lxc auth group create %s\nsudo lxc auth group permission add %s project default can_view\n", shard, shard)
		for _, n := range names[k*per : min((k+1)*per, len(names))] {
			for _, e := range soakEntitlements {
				fmt.Fprintf(&b, "sudo lxc auth group permission add %s instance %s %s project=default\n", shard, n, e)
			}
		}
		fmt.Fprintf(&b, "sudo lxc auth identity group add tls/lab-spire %s\n", shard)
	}
	return b.String()
}

// soakTeardown removes what a soak leaves: the fleet, its shard groups,
// the template and the image.
const soakTeardown = `set +e
for g in $(sudo lxc auth group list -f csv | cut -d, -f1 | grep -E '^lab-spire-s[0-9]+$'); do sudo lxc auth group delete "$g"; done
for i in $(sudo lxc list -c n -f csv 's-[0-9][0-9][0-9]$'); do sudo lxc delete --force "$i"; done
sudo lxc delete --force ` + soakTemplate + ` 2>/dev/null
sudo lxc image delete ` + soakImage + ` 2>/dev/null
true
`

// agentSetupScript installs SPIRE Agent and the agent plugin in the
// template, from the lab's artifact server and checked against the SHA-256
// the lab recorded, with the trust bundle embedded, and an OpenRC service
// that is enabled but has not run: no agent data goes into the image.
func (l *lab) agentSetupScript(t *testing.T, attestTimeout time.Duration) string {
	t.Helper()
	var env struct {
		Spire struct {
			Version string `json:"version"`
			SHA256  string `json:"sha256"`
		} `json:"spire"`
		Plugins struct {
			Version       string `json:"version"`
			AgentChecksum string `json:"agent_checksum"`
		} `json:"plugins"`
	}
	data, err := os.ReadFile(filepath.Join(l.state, "env.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	bundle := l.ssh(t, "spire", spireServerBin+" bundle show -format pem")
	spireTar := fmt.Sprintf("spire-%s-linux-amd64-musl.tar.gz", env.Spire.Version)
	pluginTar := fmt.Sprintf("lxd-agent-plugin_%s_Linux_x86_64.tar.gz", env.Plugins.Version)
	return fmt.Sprintf(`set -e
cd /tmp
until wget -q -O /dev/null http://spire.lab:8080/; do sleep 1; done
wget -q http://spire.lab:8080/%[1]s
wget -q http://spire.lab:8080/%[2]s
echo "%[3]s  %[1]s" | sha256sum -c >/dev/null
mkdir -p /opt/spire /etc/spire /var/lib/spire
tar -xzf %[1]s -C /opt/spire --strip-components=1
tar -xzf %[2]s lxd-agent-plugin
install -m 0755 lxd-agent-plugin /usr/bin/lxd-agent-plugin
echo "%[4]s  /usr/bin/lxd-agent-plugin" | sha256sum -c >/dev/null
cat > /etc/spire/bundle.pem <<'BUNDLE'
%[5]sBUNDLE
cat > /etc/spire/agent.conf <<'CONF'
agent {
  data_dir          = "/var/lib/spire/agent"
  log_level         = "INFO"
  server_address    = "spire.lab"
  server_port       = "8081"
  socket_path       = "/run/spire/agent/public/api.sock"
  trust_bundle_path = "/etc/spire/bundle.pem"
  trust_domain      = "%[6]s"
}
plugins {
  NodeAttestor "lxd_iid" {
    plugin_cmd      = "/usr/bin/lxd-agent-plugin"
    plugin_checksum = "%[4]s"
    plugin_data {
      claim {
        project = "default"
      }
      attestation_timeout = "%[7]s"
    }
  }
  KeyManager "disk" {
    plugin_data {
      directory = "/var/lib/spire/agent"
    }
  }
  WorkloadAttestor "unix" {
    plugin_data {}
  }
}
CONF
cat > /etc/init.d/spire-agent <<'RC'
#!/sbin/openrc-run
description="SPIRE Agent (lxd-spiffe lab soak)"
command="/opt/spire/bin/spire-agent"
command_args="run -config /etc/spire/agent.conf"
supervisor="supervise-daemon"
respawn_delay=3
output_log="/var/log/spire-agent.log"
error_log="/var/log/spire-agent.log"
depend() { need net; }
RC
chmod 0755 /etc/init.d/spire-agent
rc-update add spire-agent default >/dev/null
rm -f /tmp/%[1]s /tmp/%[2]s /tmp/lxd-agent-plugin
`, spireTar, pluginTar, env.Spire.SHA256, env.Plugins.AgentChecksum, bundle, labTrustDomain, attestTimeout)
}

// soakBuildImage builds the fleet's image on the server: the template
// container, set up, stopped and published as soakImage.
func (l *lab) soakBuildImage(t *testing.T, server string, attestTimeout time.Duration) {
	t.Helper()
	l.ssh(t, server, fmt.Sprintf("sudo lxc launch %s %s -q", stressImage, soakTemplate))
	setup := l.agentSetupScript(t, attestTimeout)
	l.ssh(t, server, fmt.Sprintf("sudo lxc exec %s -- sh -c %s", soakTemplate, shellQuote(setup)))
	l.ssh(t, server, fmt.Sprintf("sudo lxc stop %[1]s && sudo lxc publish %[1]s --alias %[2]s -q && sudo lxc delete %[1]s", soakTemplate, soakImage))
}

// soakFleetUp builds the image and the fleet of names on the server: the
// containers are created, the spire identity's grants given, and only then
// started, so that no agent attests before it may.
func (l *lab) soakFleetUp(t *testing.T, server string, names []string, attestTimeout time.Duration) {
	t.Helper()
	l.ssh(t, server, soakTeardown)
	t.Cleanup(func() { l.trySSH(server, soakTeardown) }) //nolint:errcheck // best effort
	if orphans := l.orphanVolumes(t, server); len(orphans) > 0 {
		t.Fatalf("%s has instance volumes without their instance: %s; run lab.sh reset", server, strings.Join(orphans, " "))
	}
	start := time.Now()
	l.soakBuildImage(t, server, attestTimeout)
	list := strings.Join(names, " ")
	l.ssh(t, server, fmt.Sprintf("printf '%%s\\n' %s | xargs -P 8 -I{} sudo lxc init %s {} -q", list, soakImage))
	l.ssh(t, server, soakShardScript(names))
	// eight starts at a time; LXD occasionally fails one under that load (a
	// veth "bad file descriptor" was seen on 6.9), so every container that
	// does not run is started again alone, up to three times
	l.ssh(t, server, fmt.Sprintf("printf '%%s\\n' %s | xargs -P 8 -I{} sudo lxc start {} 2>/dev/null; "+
		`for n in %s; do for try in 1 2 3; do [ "$(sudo lxc list -c s -f csv "^$n$")" = RUNNING ] && break; echo "starting $n again (try $try)"; sudo lxc start "$n" 2>/dev/null; sleep 2; done; `+
		`[ "$(sudo lxc list -c s -f csv "^$n$")" = RUNNING ] || { echo "$n does not run" >&2; exit 1; }; done`, list, list))
	t.Logf("%s: soak fleet of %d built in %s", server, len(names), time.Since(start).Round(time.Second))
}

// fleetUUIDs maps the fleet's instance names to their volatile.uuid.
func (l *lab) fleetUUIDs(t *testing.T, server string) map[string]string {
	t.Helper()
	var list []struct {
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	out := l.ssh(t, server, `sudo lxc list '^s-[0-9]{3}$' -f json`)
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("listing the fleet: %v", err)
	}
	uuids := map[string]string{}
	for _, i := range list {
		uuids[i.Name] = i.Config["volatile.uuid"]
	}
	return uuids
}

// fleetAgents returns the attested agents of the fleet by instance name.
func (l *lab) fleetAgents(t *testing.T, server string, uuids map[string]string) map[string]spireAgent {
	t.Helper()
	byUUID := map[string]string{}
	for n, u := range uuids {
		byUUID[l.spiffeID(&guest{server: server}, u)] = n
	}
	out := map[string]spireAgent{}
	for _, a := range l.spireAgents(t) {
		if n, ok := byUUID[a.spiffeID()]; ok {
			out[n] = a
		}
	}
	return out
}

// waitFleetAttested waits until every fleet agent is attested.
func (l *lab) waitFleetAttested(t *testing.T, server string, uuids map[string]string, d time.Duration) {
	t.Helper()
	var got int
	if !poll(d, func() bool {
		got = len(l.fleetAgents(t, server, uuids))
		return got == len(uuids)
	}) {
		t.Fatalf("%d of %d fleet agents attested within %v", got, len(uuids), d)
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

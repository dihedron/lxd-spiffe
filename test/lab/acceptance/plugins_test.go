//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// The plugins scenario's lab checks (lab test environment spec, LAB-4).

const spirePrefix = "/opt/spire"

// requirePlugins skips the test unless lab.sh selected the plugins
// scenario, and returns the lab.
func requirePlugins(t *testing.T) *lab {
	t.Helper()
	if !slices.Contains(strings.Fields(os.Getenv("LAB_SELECTED_SCENARIOS")), "plugins") {
		t.Skip("the plugins scenario is not selected (lab.sh test --scenario plugins)")
	}
	return newLab(t)
}

// plugins is the part of env.json the plugins tests read.
type plugins struct {
	VMs map[string]struct {
		IP string `json:"ip"`
	} `json:"vms"`
	Spire struct {
		Version string `json:"version"`
	} `json:"spire"`
	Plugins struct {
		Version        string `json:"version"`
		ServerChecksum string `json:"server_checksum"`
	} `json:"plugins"`
}

func (l *lab) plugins(t *testing.T) plugins {
	t.Helper()
	data, err := os.ReadFile(l.state + "/env.json")
	if err != nil {
		t.Fatal(err)
	}
	var p plugins
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// permission is one entry of an LXD group's or identity's permissions.
type permission struct {
	EntityType  string `json:"entity_type"`
	URL         string `json:"url"`
	Entitlement string `json:"entitlement"`
}

func sortedPermissions(p []permission) []string {
	out := make([]string, 0, len(p))
	for _, e := range p {
		out = append(out, e.EntityType+" "+e.URL+" "+e.Entitlement)
	}
	slices.Sort(out)
	return out
}

// TestLAB4Spire: SPIRE Server is healthy with the plugin loaded from its
// checksum; the spire identity reaches every LXD server with exactly the
// lab-spire grants; a guest resolves and reaches spire.lab.
func TestLAB4Spire(t *testing.T) {
	l := requirePlugins(t)
	p := l.plugins(t)
	if out, err := l.trySSH("spire", "sudo "+spirePrefix+"/bin/spire-server healthcheck"); err != nil {
		t.Fatalf("SPIRE Server %s is not healthy: %v %s", p.Spire.Version, err, out)
	}
	installed := strings.Fields(l.ssh(t, "spire", "sha256sum /usr/bin/lxd-server-plugin"))[0]
	if installed != p.Plugins.ServerChecksum || !strings.Contains(l.ssh(t, "spire", "cat /etc/spire/server.conf"), `plugin_checksum = "`+installed+`"`) {
		t.Errorf("lxd-server-plugin %s, configured checksum or env.json %s differ", installed, p.Plugins.ServerChecksum)
	}
	if log := l.ssh(t, "spire", "sudo journalctl -u spire-server --no-pager -b"); !strings.Contains(log, "Plugin loaded") || !strings.Contains(log, "lxd_iid") {
		t.Errorf("SPIRE Server's log shows no lxd_iid plugin loaded:\n%s", log)
	}
	s := check(t)
	for _, server := range append(l.servers(), l.clusters()...) {
		t.Run(server, func(t *testing.T) {
			r := l.probe(t, s, server, "spire", "server", "whoami")
			groups, _ := r.observation("identity.groups")
			if r.code != 0 || fmt.Sprint(groups) != "[lab-spire]" {
				t.Fatalf("whoami as spire: groups %v, want [lab-spire]: %s", groups, r)
			}
			raw, _ := r.observation("identity.effective_permissions")
			var got []permission
			if data, err := json.Marshal(raw); err != nil || json.Unmarshal(data, &got) != nil {
				t.Fatalf("effective permissions %v", raw)
			}
			var group struct {
				Permissions []permission `json:"permissions"`
			}
			if err := json.Unmarshal([]byte(l.ssh(t, server, "sudo lxc query /1.0/auth/groups/lab-spire")), &group); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(sortedPermissions(got), sortedPermissions(group.Permissions)) || !slices.Contains(sortedPermissions(got), "project /1.0/projects/default can_view") || !slices.Contains(sortedPermissions(got), "project /1.0/projects/default can_view_instances") {
				t.Errorf("spire's permissions %v, lab-spire's %v", sortedPermissions(got), sortedPermissions(group.Permissions))
			}
			for _, e := range got {
				if e.EntityType == "server" || e.EntityType == "instance" && !slices.Contains([]string{"can_view", "can_access_files", "can_edit"}, e.Entitlement) {
					t.Errorf("spire holds %v, beyond the attestor's least privilege", e)
				}
			}
		})
	}
	for _, server := range l.servers() {
		out, err := l.trySSH(server, "sudo lxc exec c-unpriv -- sh -c 'getent hosts spire.lab && curl -fsS -o /dev/null http://spire.lab:8080/ && timeout 5 bash -c \"</dev/tcp/spire.lab/8081\"'")
		if err != nil {
			t.Errorf("%s: c-unpriv cannot reach spire.lab on 8080 and 8081: %v %s", server, err, out)
		}
	}
}

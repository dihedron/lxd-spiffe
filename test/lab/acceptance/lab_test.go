//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// check is the session of a lab check: its evidence is kept only for the
// test's duration.
func check(t *testing.T) session {
	return session{evidenceDir: t.TempDir(), runID: "lab-check"}
}

// TestLAB1Servers: every server answers server info with its recorded pin
// (the connection checks it), version and extension hash; a server that
// accepts TLS 1.2 is a finding for Q3, not a failure.
func TestLAB1Servers(t *testing.T) {
	l := newLab(t)
	s := check(t)
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			want := l.env.Servers[server]
			r := l.probe(t, s, server, "full", "server", "info", "--probe-tls12")
			if r.code != 0 || r.rec.Server == nil {
				t.Fatalf("server info: %s", r)
			}
			if r.rec.Server.Version != want.Version || r.rec.Server.APIExtensionsHash != want.APIExtensionsHash {
				t.Errorf("server info reports %s %s; env.json records %s %s",
					r.rec.Server.Version, r.rec.Server.APIExtensionsHash, want.Version, want.APIExtensionsHash)
			}
			if accepted, _ := r.observation("tls12.accepted"); accepted == true {
				t.Logf("finding (Q3): %s accepts TLS 1.2", server)
			}
		})
	}
}

// TestLAB2Identities: each identity sees exactly its own group; read sees an
// instance of default; outsider gets 404 in restricted, as for a missing
// instance; an untrusted certificate gets 403.
func TestLAB2Identities(t *testing.T) {
	l := newLab(t)
	s := check(t)
	cert, key := untrustedCert(t)
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			for _, id := range l.identities(server) {
				group := "lab-" + id
				if id == "bearer" {
					group = "lab-files"
				}
				r := l.probe(t, s, server, id, "server", "whoami")
				groups, _ := r.observation("identity.groups")
				if r.code != 0 || fmt.Sprint(groups) != fmt.Sprint([]any{group}) {
					t.Errorf("whoami as %s: groups %v, want [%s]: %s", id, groups, group, r)
				}
			}
			expectOK := func(what string, r *run) {
				t.Helper()
				if r.code != 0 || r.rec.Outcome != evidence.OutcomeOK {
					t.Errorf("%s: %s", what, r)
				}
			}
			expectOK("read: instance show c-unpriv",
				l.probe(t, s, server, "read", "server", "instance", "show", "c-unpriv"))
			expectOK("outsider: instance show c-unpriv in restricted, --expect-hidden",
				l.probe(t, s, server, "outsider", "server", "instance", "show", "c-unpriv", "--project", "restricted", "--expect-hidden"))
			expectOK("untrusted certificate: instance show c-unpriv, --expect-denied",
				l.probe(t, s, server, "full", "server", "instance", "show", "c-unpriv", "--client-cert", cert, "--client-key", key, "--expect-denied"))
		})
	}
}

// baselineInstance is one row of the spec's table of baseline instances.
type baselineInstance struct {
	project, name, kind, os string
	config                  map[string]string
}

// baseline returns the spec's baseline instances, with the guest VM sizes
// lab.sh exports.
func baseline() []baselineInstance {
	vm := func(extra ...string) map[string]string {
		c := map[string]string{
			"limits.cpu":    setting("LAB_GUEST_VM_VCPUS", "1"),
			"limits.memory": setting("LAB_GUEST_VM_RAM_GIB", "1") + "GiB",
		}
		for i := 0; i+1 < len(extra); i += 2 {
			c[extra[i]] = extra[i+1]
		}
		return c
	}
	return []baselineInstance{
		{"default", "c-unpriv", "container", "ubuntu", nil},
		{"default", "c-priv", "container", "ubuntu", map[string]string{"security.privileged": "true", "security.nesting": "true"}},
		{"default", "c-iso", "container", "ubuntu", map[string]string{"security.idmap.isolated": "true"}},
		{"default", "c-raw", "container", "ubuntu", map[string]string{"raw.idmap": "both 1000 1000"}},
		{"default", "v-ubuntu", "virtual-machine", "ubuntu", vm()},
		{"default", "v-debian", "virtual-machine", "debian", vm()},
		{"default", "v-alpine", "virtual-machine", "alpine", vm()},
		{"restricted", "c-unpriv", "container", "ubuntu", nil},
	}
}

// labInstance is the part of an LXD instance LAB-3 checks.
type labInstance struct {
	Name            string                       `json:"name"`
	Project         string                       `json:"project"`
	Type            string                       `json:"type"`
	Status          string                       `json:"status"`
	Config          map[string]string            `json:"config"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
}

// TestLAB3Baseline: every baseline instance runs, with the configuration of
// the spec's table and the lxd-probe that deploy built.
func TestLAB3Baseline(t *testing.T) {
	l := newLab(t)
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			var instances []labInstance
			out := l.ssh(t, server, "sudo lxc query '/1.0/instances?recursion=1&all-projects=true'")
			if err := json.Unmarshal([]byte(out), &instances); err != nil {
				t.Fatalf("instances: %v", err)
			}
			for _, want := range baseline() {
				i := slices.IndexFunc(instances, func(i labInstance) bool {
					return i.Project == want.project && i.Name == want.name
				})
				where := want.project + "/" + want.name
				if i < 0 {
					t.Errorf("%s: missing", where)
					continue
				}
				got := instances[i]
				if got.Status != "Running" || got.Type != want.kind {
					t.Errorf("%s: %s %s; want a running %s", where, got.Status, got.Type, want.kind)
				}
				if imageOS := strings.ToLower(got.Config["image.os"]); imageOS != want.os {
					t.Errorf("%s: image.os %q; want %s", where, got.Config["image.os"], want.os)
				}
				for k, v := range want.config {
					if got.Config[k] != v {
						t.Errorf("%s: %s = %q; want %q", where, k, got.Config[k], v)
					}
				}
				if want.name == "v-alpine" && got.Config["security.secureboot"] != "false" && got.Config["boot.mode"] != "uefi-nosecureboot" {
					t.Errorf("%s: secure boot is on", where)
				}
				bridge := "lxdbr0"
				if want.project == "restricted" {
					bridge = "lxdbr1"
				}
				if n := got.ExpandedDevices["eth0"]["network"]; n != bridge {
					t.Errorf("%s: eth0 on %q; want %s", where, n, bridge)
				}
				sum := l.ssh(t, server, fmt.Sprintf("sudo lxc exec %s --project %s -- sha256sum /usr/local/bin/lxd-probe", want.name, want.project))
				if f := strings.Fields(sum); len(f) == 0 || f[0] != l.env.Probe.SHA256 {
					t.Errorf("%s: lxd-probe %q; want %s (lab.sh deploy)", where, strings.TrimSpace(sum), l.env.Probe.SHA256)
				}
			}
		})
	}
}

//go:build lab

package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleL16 is gate L16's decision rule (probe spec, Gate runbooks).
const ruleL16 = "L16 decision: (1) if l16-min's reads fail where l16-view's pass, project can_view joins every mode of the attestor's " +
	"Least privilege table and selectors.network_project needs no further entitlement; otherwise the table stands and the option adds " +
	"project can_view. (2) The entitlement that shows a project's config; if only a server-level grant does, selectors.network_project " +
	"is withdrawn. The project fixtures become the contract of the project read."

// l16Identity is a session identity of G-L16: its group's permissions and
// its certificate.
type l16Identity struct {
	name        string
	permissions []string // "entity_type entity entitlement [key=value]", for lxc auth group permission add
	cert, key   string
}

// TestGL16ProjectEntitlements is G-L16, gate L16's session: on each server,
// the session's identities l16-min (project can_view_instances and
// instance can_view and can_access_files on c-unpriv, no project
// can_view) and l16-view (the same plus project can_view on default,
// l16-on and l16-off), and the projects l16-on (features.networks=true)
// and l16-off (false), created here and deleted at the end; as each: the
// instance read, a file stat, the uuid filter and project show of the
// three projects; the provocations of project show (missing, hidden,
// untrusted). With both clusters up, project show default through each
// member. Project show records its fixtures; with LAB_COLLECT=yes they go
// to test/fixtures/<version>/projects/.
func TestGL16ProjectEntitlements(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l16-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			fixtures := filepath.Join(l.evidence, runID, "fixtures")
			instancePerms := []string{"instance c-unpriv can_view project=default", "instance c-unpriv can_access_files project=default"}
			ids := []*l16Identity{
				{name: "l16-min", permissions: append([]string{"project default can_view_instances"}, instancePerms...)},
				{name: "l16-view", permissions: append([]string{"project default can_view_instances",
					"project default can_view", "project l16-on can_view", "project l16-off can_view"}, instancePerms...)},
			}
			l.l16Setup(t, server, ids)

			uuid := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get c-unpriv volatile.uuid"))
			as := func(id *l16Identity, cell string, args ...string) *run {
				full := append(args, "--gate", "L16", "--cell", server+"-"+cell, "--client-cert", id.cert, "--client-key", id.key)
				return l.probe(t, s, server, "full", full...)
			}
			status := func(r *run, call string) string {
				st, _ := r.observation(call + ".http_status")
				return fmt.Sprint(st)
			}
			t.Logf("  %-9s %-9s %-9s %-9s %-24s %-24s %-24s", "identity", "instance", "file", "filter", "project default", "project l16-on", "project l16-off")
			for _, id := range ids {
				inst := as(id, id.name, "server", "instance", "show", "c-unpriv")
				file := as(id, id.name, "server", "file", "stat", "c-unpriv", "/etc/hostname")
				list := as(id, id.name, "server", "instance", "list", "--filter", "config.volatile.uuid eq "+uuid)
				count, _ := list.observation("instances.count")
				row := []string{status(inst, "instance"), status(file, "file"), fmt.Sprintf("%s (%v)", status(list, "list"), count)}
				for _, project := range []string{"default", "l16-on", "l16-off"} {
					p := as(id, id.name, "server", "project", "show", project, "--record", fixtures)
					networks, _ := p.observation("project.features.networks")
					row = append(row, fmt.Sprintf("%s networks=%v", status(p, "project"), networks))
				}
				t.Logf("  %-9s %-9s %-9s %-9s %-24s %-24s %-24s", id.name, row[0], row[1], row[2], row[3], row[4], row[5])
			}
			missing := l.probe(t, s, server, "full", "server", "project", "show", "l16-missing", "--gate", "L16", "--cell", server+"-full", "--record", fixtures)
			hidden := l.probe(t, s, server, "outsider", "server", "project", "show", "restricted", "--expect-hidden", "--gate", "L16", "--cell", server+"-outsider", "--record", fixtures)
			cert, key := untrustedCert(t)
			denied := l.probe(t, s, server, "full", "server", "project", "show", "default", "--expect-denied", "--client-cert", cert, "--client-key", key,
				"--gate", "L16", "--cell", server+"-untrusted", "--record", filepath.Join(fixtures, "untrusted"))
			t.Logf("  missing project: %s; restricted as outsider: %s; untrusted: %s", status(missing, "project"), status(hidden, "project"), status(denied, "project"))

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL16)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
				l.collectFixtures(t, server, "projects", "standalone", staging)
			}
		})
	}
	for _, cluster := range l.clusters() {
		t.Run(cluster, func(t *testing.T) {
			runID := stamp + "-g-l16-" + cluster
			s := session{evidenceDir: l.evidence, runID: runID}
			fixtures := filepath.Join(l.evidence, runID, "fixtures")
			for _, member := range l.members(cluster) {
				r := l.probeMember(t, s, cluster, member, "full", "server", "project", "show", "default",
					"--gate", "L16", "--cell", cluster+"-"+member, "--record", fixtures)
				st, _ := r.observation("project.http_status")
				networks, _ := r.observation("project.features.networks")
				t.Logf("  through %s: %v networks=%v", member, st, networks)
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
				l.collectFixtures(t, cluster, "projects", "cluster", staging)
			}
		})
	}
}

// l16Setup creates the session's projects, groups and identities on
// server, and deletes them when the test ends, whatever happened.
func (l *lab) l16Setup(t *testing.T, server string, ids []*l16Identity) {
	t.Helper()
	teardown := "sudo lxc auth identity delete tls/l16-min 2>/dev/null; sudo lxc auth identity delete tls/l16-view 2>/dev/null; " +
		"sudo lxc auth group delete l16-min 2>/dev/null; sudo lxc auth group delete l16-view 2>/dev/null; " +
		"sudo lxc project delete l16-on 2>/dev/null; sudo lxc project delete l16-off 2>/dev/null; true"
	l.ssh(t, server, teardown)
	t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort
	l.ssh(t, server, "sudo lxc project create l16-on -c features.networks=true && sudo lxc project create l16-off -c features.networks=false")
	for _, id := range ids {
		id.cert, id.key = clientCert(t, id.name)
		script := "set -e; sudo lxc auth group create " + id.name
		for _, p := range id.permissions {
			script += "; sudo lxc auth group permission add " + id.name + " " + p
		}
		l.ssh(t, server, script)
		pemData, err := os.ReadFile(id.cert)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"name": id.name, "certificate": string(pemData), "groups": []string{id.name}})
		l.ssh(t, server, `sudo lxc query -X POST /1.0/auth/identities/tls -d "$(echo `+base64.StdEncoding.EncodeToString(body)+` | base64 -d)" >/dev/null`)
	}
}

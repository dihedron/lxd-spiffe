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

// ruleL17 is gate L17's decision rule (probe spec, Gate runbooks).
const ruleL17 = "L17 decision: if every grant is honoured, Least privilege gains a sharding rule (per-instance grants spread over " +
	"groups of at most 90, each also holding project can_view, the SPIRE identity a member of all of them); if authorization fails " +
	"beyond about 100 effective permissions, one identity's per-instance grants cap at about 95 instances, and the attestor spec " +
	"chooses between a project-wide entitlement in a dedicated restricted project and several SPIRE identities."

// l17Fleet is the session's fleet size per server, and l17Groups the
// number of groups its grants are spread over.
const (
	l17Fleet  = 120
	l17Groups = 3
)

func l17Name(i int) string { return fmt.Sprintf("l17-%03d", i) }

// TestGL17Sharding is G-L17, gate L17's session (probe spec runbook): on
// each server, the limit of permissions per group, then an identity whose
// grants are spread over three groups, about 250 in all, checked on
// instances of every group.
func TestGL17Sharding(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l17-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			cert, key := l.l17Setup(t, server)
			note := func(text string) {
				l.util(t, s, "note", "--gate", "L17", "--cell", server+"-limit", text)
			}

			// (1) the limit: instance can_view grants until LXD refuses one
			limit := `set +e; n=1; sudo lxc auth group permission add l17-limit project default can_view >/dev/null; ` +
				`for i in $(seq -f "l17-%03g" 1 ` + fmt.Sprint(l17Fleet) + `); do ` +
				`out=$(sudo lxc auth group permission add l17-limit instance "$i" can_view project=default 2>&1) || { echo "refused #$((n+1)): $out"; exit 0; }; n=$((n+1)); done; echo "accepted all $n"`
			res := strings.TrimSpace(l.ssh(t, server, limit))
			t.Logf("  limit: %s", res)
			note("group l17-limit, project can_view then instance can_view grants one by one: " + res)

			// (2) sharding: the identity of three groups
			as := func(cell string, args ...string) *run {
				full := append(args, "--gate", "L17", "--cell", server+"-"+cell, "--client-cert", cert, "--client-key", key)
				return l.probe(t, s, server, "full", full...)
			}
			who := as("shard", "server", "whoami")
			perms, _ := who.observation("identity.effective_permissions")
			groups, _ := who.observation("identity.groups")
			n := 0
			if p, ok := perms.([]any); ok {
				n = len(p)
			}
			t.Logf("  l17-shard: groups %v, %d effective permissions", groups, n)
			status := func(r *run, call string) string {
				st, _ := r.observation(call + ".http_status")
				return fmt.Sprint(st)
			}
			per := l17Fleet / l17Groups
			for g := range l17Groups {
				first, last := g*per+1, (g+1)*per
				for _, i := range []int{first, last} {
					name := l17Name(i)
					show := as(fmt.Sprintf("g%d", g+1), "server", "instance", "show", name)
					stat := as(fmt.Sprintf("g%d", g+1), "server", "file", "stat", name, "/etc/hostname")
					t.Logf("  group %d %s: instance %s, file %s", g+1, name, status(show, "instance"), status(stat, "file"))
				}
				edit := l17Name(first)
				set := as(fmt.Sprintf("g%d", g+1), "server", "config", "set", edit, "user.lxd-probe.l17", "x", "--method", "patch", "--if-match", "auto", "--allow-write", "--lab")
				unset := as(fmt.Sprintf("g%d", g+1), "server", "config", "unset", edit, "user.lxd-probe.l17", "--method", "patch-null", "--allow-write", "--lab")
				t.Logf("  group %d %s: PATCH set %s, unset %s", g+1, edit, status(set, "write"), status(unset, "write"))
			}
			uuid := strings.TrimSpace(l.ssh(t, server, "sudo lxc config get "+l17Name(l17Fleet)+" volatile.uuid"))
			list := as("shard", "server", "instance", "list", "--filter", "config.volatile.uuid eq "+uuid)
			count, _ := list.observation("instances.count")
			// the control: project can_view_instances shows every instance, so
			// the ungranted action is a file read, denied (403)
			denied := as("control", "server", "file", "stat", "c-unpriv", "/etc/hostname", "--expect-denied")
			t.Logf("  filter on %s: %s (%v found); control, file of c-unpriv: %s", l17Name(l17Fleet), status(list, "list"), count, status(denied, "file"))
			if denied.rec.Outcome != "ok" {
				t.Errorf("control: %s", denied)
			}

			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL17)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

// l17Setup builds the session's fleet, groups and identity on server, and
// deletes them when the test ends, and first if a previous session left
// them. It returns the identity's certificate and key.
func (l *lab) l17Setup(t *testing.T, server string) (cert, key string) {
	t.Helper()
	teardown := `set +e; sudo lxc auth identity delete tls/l17-shard 2>/dev/null; ` +
		`for g in l17-limit l17-g1 l17-g2 l17-g3; do sudo lxc auth group delete $g 2>/dev/null; done; ` +
		`for i in $(sudo lxc list -c n -f csv "l17-[0-9][0-9][0-9]$"); do sudo lxc delete --force "$i"; done; true`
	l.ssh(t, server, teardown)
	t.Cleanup(func() { l.trySSH(server, teardown) }) //nolint:errcheck // best effort

	start := time.Now()
	names := make([]string, l17Fleet)
	for i := range names {
		names[i] = l17Name(i + 1)
	}
	list := strings.Join(names, " ")
	l.ssh(t, server, fmt.Sprintf(`printf '%%s\n' %s | xargs -P 8 -I{} sudo lxc launch %s {} -q 2>/dev/null; `+
		`for n in %s; do for try in 1 2 3; do [ "$(sudo lxc list -c ns -f csv "^$n$")" = "$n,RUNNING" ] && break; `+
		`sudo lxc delete --force "$n" 2>/dev/null; sudo lxc launch %s "$n" -q; sleep 2; done; done`, list, stressImage, list, stressImage))
	t.Logf("%s: fleet of %d built in %s", server, l17Fleet, time.Since(start).Round(time.Second))

	var script strings.Builder
	script.WriteString("set -e\nsudo lxc auth group create l17-limit\n")
	per := l17Fleet / l17Groups
	for g := range l17Groups {
		group := fmt.Sprintf("l17-g%d", g+1)
		fmt.Fprintf(&script, "sudo lxc auth group create %s\nsudo lxc auth group permission add %s project default can_view\n", group, group)
		if g == 0 {
			fmt.Fprintf(&script, "sudo lxc auth group permission add %s project default can_view_instances\n", group)
		}
		for i := g*per + 1; i <= (g+1)*per; i++ {
			for _, e := range []string{"can_view", "can_access_files"} {
				fmt.Fprintf(&script, "sudo lxc auth group permission add %s instance %s %s project=default\n", group, l17Name(i), e)
			}
		}
		for i := g*per + 1; i <= g*per+2; i++ {
			fmt.Fprintf(&script, "sudo lxc auth group permission add %s instance %s can_edit project=default\n", group, l17Name(i))
		}
	}
	l.ssh(t, server, script.String())

	cert, key = clientCert(t, "l17-shard")
	pemData, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "l17-shard", "certificate": string(pemData), "groups": []string{"l17-g1", "l17-g2", "l17-g3"}})
	l.ssh(t, server, `sudo lxc query -X POST /1.0/auth/identities/tls -d "$(echo `+base64.StdEncoding.EncodeToString(body)+` | base64 -d)" >/dev/null`)
	return cert, key
}

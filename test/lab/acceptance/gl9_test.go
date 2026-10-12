//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ruleL9 is gate L9's decision rule (probe spec, Gate runbooks).
const ruleL9 = "L9 decision: the minimal entitlement set per proof mode (attestor spec, Privilege cost of each mode); " +
	"whether an identity can see its own privileges (the SEC-16 warning is feasible or not)."

// TestGL9Entitlements is G-L9, gate L9's session: on each server, as each
// probe identity (and bearer where there is one), server whoami and
// server permissions on c-unpriv in default and in restricted, one run per
// server. It prints the matrix next to the decision rule, fails only when
// the probe fails or the evidence is not sanitized, and with
// LAB_COLLECT=yes copies each run to docs/validation/.
func TestGL9Entitlements(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	calls := []string{"instance", "state", "files", "events", "operations", "identity"}
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l9-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			t.Logf("  %-9s %-10s %-26s %v", "identity", "project", "groups (whoami)", calls)
			for _, id := range l.identities(server) {
				tags := []string{"--gate", "L9", "--cell", server + "-" + id}
				who := l.probe(t, s, server, id, append([]string{"server", "whoami"}, tags...)...)
				groups, _ := who.observation("identity.groups")
				for _, project := range []string{"default", "restricted"} {
					p := l.probe(t, s, server, id, append([]string{"server", "permissions", "c-unpriv", "--project", project}, tags...)...)
					v, _ := p.observation("permissions")
					var m map[string]string
					data, _ := json.Marshal(v)
					_ = json.Unmarshal(data, &m)
					row := make([]string, len(calls))
					for i, c := range calls {
						row[i] = m[c]
					}
					t.Logf("  %-9s %-10s %-26s %v", id, project, fmt.Sprint(groups), row)
				}
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL9)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

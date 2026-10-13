//go:build lab

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ruleL13 is gate L13's decision rule (probe spec, Gate runbooks).
const ruleL13 = "L13 decision: whether the plugin can run without long-lived client certificates; " +
	"the expiry behaviour determines the rotation requirements."

// TestGL13Bearer is G-L13, gate L13's session, one run per server: on a
// server with bearer identities, whoami and bearer check with lab-bearer's
// long-lived token; a 2-minute token from lab.sh token, checked with
// --wait-expiry; and a token checked with --expect-denied after a second
// one was issued for the same identity (revocation). A server without
// bearer identities gets a util note saying so.
func TestGL13Bearer(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	for _, server := range l.servers() {
		t.Run(server, func(t *testing.T) {
			runID := stamp + "-g-l13-" + server
			s := session{evidenceDir: l.evidence, runID: runID}
			tags := func(cell string) []string { return []string{"--gate", "L13", "--cell", server + "-" + cell} }
			if l.env.Servers[server].Auth.Bearer != "yes" {
				version := l.env.Servers[server].Version
				l.util(t, s, append([]string{"note", fmt.Sprintf("LXD %s on %s offers no bearer identities (no auth_bearer extension): L13 cannot be tested here", version, server)}, tags("none")...)...)
				t.Logf("%s (LXD %s): no bearer identities, recorded as a note", server, version)
			} else {
				who := l.probe(t, s, server, "bearer", append([]string{"server", "whoami"}, tags("bearer")...)...)
				long := l.probe(t, s, server, "bearer", append([]string{"server", "bearer", "check"}, tags("bearer")...)...)
				claims, _ := long.observation("token.claims")
				method, _ := who.observation("identity.authentication_method")
				t.Logf("long-lived token: %v; whoami %v", claims, method)

				l.labsh(t, "token", server, "--expiry", "2M")
				short := l.probe(t, s, server, "bearer-short", append([]string{"server", "bearer", "check", "--wait-expiry", "--max-wait", "5m"}, tags("bearer-short")...)...)
				last, _ := short.observation("expiry.last_success")
				refusal, _ := short.observation("expiry.first_refusal")
				t.Logf("2-minute token: last success %v; first refusal %v (outcome %s)", last, refusal, short.rec.Outcome)

				// revocation: keep the first token, issue a second
				l.labsh(t, "token", server, "--expiry", "10M")
				first := filepath.Join(t.TempDir(), "first.token")
				data, err := os.ReadFile(filepath.Join(l.state, "tokens", server+"-bearer-short.token"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(first, data, 0o600); err != nil {
					t.Fatal(err)
				}
				l.labsh(t, "token", server, "--expiry", "10M")
				revoked := l.probe(t, s, server, "bearer-short", append([]string{"server", "bearer", "check", "--token-file", first, "--expect-denied"}, tags("bearer-short-revoked")...)...)
				current := l.probe(t, s, server, "bearer-short", append([]string{"server", "bearer", "check"}, tags("bearer-short")...)...)
				rc, _ := revoked.observation("bearer.calls")
				cc, _ := current.observation("bearer.calls")
				t.Logf("revoked token: %v (outcome %s); its successor: %v", rc, revoked.rec.Outcome, cc)
			}
			runDir := filepath.Join(l.evidence, runID)
			staging := filepath.Join(t.TempDir(), runID)
			l.sanitize(t, runDir, staging)
			l.sanitizeCheck(t, staging)
			t.Logf("run %s, in %s", runID, runDir)
			t.Log(ruleL13)
			if collect && !t.Failed() {
				l.collectRun(t, runID, staging)
			}
		})
	}
}

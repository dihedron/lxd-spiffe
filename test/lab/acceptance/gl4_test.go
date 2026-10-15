//go:build lab

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleL4 is gate L4's decision rule (probe spec, Gate runbooks).
const ruleL4 = "L4 decision: if non-root users can use devLXD, config_push challenge data is readable by any local " +
	"process: the challenge is not secret and proof strength relies on the nonce binding only (add the threat entry); " +
	"if only root can, record it as a control. The cloud-init path answers who can read cloud-init data through devLXD " +
	"(attestor spec, Alternatives: Keys delivered through cloud-init)."

// TestGL4DevLXDAccess is G-L4, gate L4's session: as root in an
// unprivileged container, a privileged container and a VM of each server,
// guest preflight and guest devlxd access as uids 0, 1000 and 65534 on
// /1.0 and cloud-init.user-data, one run per server and instance. It
// prints who may do what next to the decision rule, fails only when the
// probe fails or the evidence is not sanitized, and with LAB_COLLECT=yes
// copies each run to docs/validation/.
func TestGL4DevLXDAccess(t *testing.T) {
	l := newLab(t)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	collect := os.Getenv("LAB_COLLECT") == "yes"
	uids := []string{"0", "1000", "65534"}
	paths := []string{"/1.0", "/1.0/config/cloud-init.user-data"}
	for _, server := range l.servers() {
		for _, name := range []string{"c-unpriv", "c-priv", "v-ubuntu"} {
			t.Run(server+"/"+name, func(t *testing.T) {
				runID := stamp + "-g-l4-" + server + "-" + name
				s := session{runID: runID}
				cell := server + "-" + name
				l.guest(t, s, server, "default", name, "guest", "preflight", "--gate", "L4", "--cell", cell)
				args := []string{"guest", "devlxd", "access", "--gate", "L4", "--cell", cell, "--as-uid", strings.Join(uids, ",")}
				for _, p := range paths {
					args = append(args, "--path", p)
				}
				access := l.guest(t, s, server, "default", name, args...)
				l.pull(t, server, "default", name, runID)

				runDir := filepath.Join(l.evidence, runID)
				staging := filepath.Join(t.TempDir(), runID)
				l.sanitize(t, runDir, staging)
				l.sanitizeCheck(t, staging)
				t.Logf("run %s, in %s", runID, runDir)
				for _, uid := range uids {
					v, ok := access.observation("access.uid." + uid)
					if !ok {
						t.Errorf("no observation for uid %s: %s", uid, access)
						continue
					}
					t.Logf("  uid %-5s %s", uid, accessSummary(v))
				}
				t.Log(ruleL4)
				if collect && !t.Failed() {
					t.Logf("collected: %s", l.collectRun(t, runID, staging))
				}
			})
		}
	}
}

// accessSummary renders an access.uid.<n> observation as "path: allowed |
// denied (status, error)", one per path.
func accessSummary(v any) string {
	var obs struct {
		Error   string `json:"error"`
		Results []struct {
			Path    string `json:"path"`
			Status  int    `json:"status"`
			Error   string `json:"error"`
			Allowed bool   `json:"allowed"`
		} `json:"results"`
	}
	data, _ := json.Marshal(v)
	if err := json.Unmarshal(data, &obs); err != nil {
		return fmt.Sprint(v)
	}
	if obs.Error != "" {
		return "child failed: " + obs.Error
	}
	var parts []string
	for _, r := range obs.Results {
		verdict := "allowed"
		if !r.Allowed {
			verdict = fmt.Sprintf("denied (%d %s)", r.Status, r.Error)
		}
		parts = append(parts, r.Path+": "+verdict)
	}
	return strings.Join(parts, "; ")
}

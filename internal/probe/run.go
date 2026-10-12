package probe

import (
	"fmt"
	"os"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/pkg/metadata"
)

// RedactedHost is what run.json records as the host name without
// --record-host (PRE-03).
const RedactedHost = "redacted"

// OpenRun opens the run directory of env for this invocation (PRE-07),
// generating the run identifier when none is given. endpoint is the LXD
// endpoint of a server run, recorded in run.json without credentials; the
// host name is recorded only with --record-host.
func (g *GlobalOptions) OpenRun(env, endpoint string) (*evidence.Run, error) {
	if g.RunID == "" {
		id, err := evidence.NewRunID(time.Now())
		if err != nil {
			return nil, err
		}
		g.RunID = id
	}
	host := RedactedHost
	if g.RecordHost {
		host, _ = os.Hostname()
	}
	run, err := evidence.Open(g.EvidenceDir, g.RunID, env, evidence.RunInfo{
		ToolVersion: metadata.Version,
		GitCommit:   metadata.GitCommit,
		Host:        host,
		Endpoint:    endpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("evidence: %w", err)
	}
	return run, nil
}

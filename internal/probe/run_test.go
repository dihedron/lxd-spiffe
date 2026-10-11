package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

func TestOpenRun(t *testing.T) {
	g := &GlobalOptions{EvidenceDir: t.TempDir()}
	run, err := g.OpenRun("server", "https://lab:8443")
	if err != nil {
		t.Fatal(err)
	}
	if g.RunID == "" || run.ID != g.RunID || run.Dir != filepath.Join(g.EvidenceDir, g.RunID, "server") {
		t.Fatalf("run %+v, run id %q", run, g.RunID)
	}
	data, err := os.ReadFile(filepath.Join(run.Dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info evidence.RunInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Endpoint != "https://lab:8443" || info.Env != "server" || info.ToolVersion == "" {
		t.Errorf("run.json = %+v", info)
	}

	// a second invocation with the same run id reuses the directory
	again, err := (&GlobalOptions{EvidenceDir: g.EvidenceDir, RunID: g.RunID}).OpenRun("server", "")
	if err != nil || again.Dir != run.Dir {
		t.Errorf("reopen: %v, %v", again, err)
	}
	if _, err := (&GlobalOptions{EvidenceDir: g.EvidenceDir, RunID: "../x"}).OpenRun("server", ""); err == nil {
		t.Errorf("bad run id accepted")
	}
}

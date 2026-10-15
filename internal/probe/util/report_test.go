package util

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// reportRun makes a run with one decision, through util note.
func reportRun(t *testing.T) (root, run string) {
	t.Helper()
	opts := utilOpts(t)
	opts.Gate, opts.Cell = "L4", "all"
	cmd := &Note{GlobalOptions: opts, Decision: true, stdout: &bytes.Buffer{}}
	cmd.Args.Text = "only root can use devLXD: recorded as a control"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	return opts.EvidenceDir, filepath.Join(opts.EvidenceDir, opts.RunID)
}

// snapshot lists the files under dir, to show the report writes none.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var files []string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(files, "\n")
}

func TestReport(t *testing.T) {
	root, run := reportRun(t)
	before := snapshot(t, root)

	// one document on stdout
	out := &bytes.Buffer{}
	cmd := &Report{GlobalOptions: utilOpts(t), stdout: out}
	cmd.EvidenceDir = root
	cmd.Args.RunDirs = []string{run}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| L4 | Who can open `/dev/lxd/sock` | decided | 1 | 1 | 1 |",
		"# L4 — Who can open `/dev/lxd/sock`",
		"- `r1/util/0001` (",
		"only root can use devLXD: recorded as a control",
		"# L14 — Instance update semantics",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if after := snapshot(t, root); after != before {
		t.Errorf("the report wrote into the evidence:\n%s", after)
	}

	// one file per gate
	dir := filepath.Join(t.TempDir(), "report")
	out.Reset()
	cmd.Out = dir
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.md", "L1.md", "L4.md", "L15.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	if !strings.Contains(out.String(), "index.md") {
		t.Errorf("stdout %q: names the index", out.String())
	}
	if after := snapshot(t, root); after != before {
		t.Errorf("the report wrote into the evidence:\n%s", after)
	}

	// not a run directory
	for _, bad := range []string{t.TempDir(), filepath.Join(root, "missing")} {
		cmd := &Report{GlobalOptions: utilOpts(t), stdout: &bytes.Buffer{}}
		cmd.Args.RunDirs = []string{bad}
		if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
			t.Errorf("%s: exit %d (%v)", filepath.Base(bad), exit.Code(err), err)
		}
	}
}

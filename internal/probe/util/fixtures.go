package util

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/fixturecheck"
)

// FixturesVerify is util fixtures verify DIR... (PRU-06): every fixture
// decodes into the type the probe decodes it into and carries the fields
// of the attestor's minimal API contract; errors are LXD error envelopes.
type FixturesVerify struct {
	probe.GlobalOptions
	Args struct {
		Dirs []string `positional-arg-name:"DIR" description:"Fixture directories (test/fixtures/<lxd-version>)." required:"1"`
	} `positional-args:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *FixturesVerify) Execute(args []string) error {
	// directory names only: a full path may hold a user's name
	var recorded []string
	for _, dir := range cmd.Args.Dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return exit.New(exit.Usage, fmt.Errorf("util fixtures verify: %s is not a directory", filepath.Base(dir)))
		}
		recorded = append(recorded, filepath.Base(dir))
	}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "fixtures verify", recorded)
	if err != nil {
		return err
	}
	var all []fixturecheck.Result
	perDir := map[string]any{}
	for _, dir := range cmd.Args.Dirs {
		results, err := fixturecheck.Verify(dir)
		if err != nil {
			return s.Finish(err)
		}
		for i := range results {
			results[i].Fixture = filepath.Base(dir) + "/" + results[i].Fixture
		}
		sum := fixturecheck.Summarize(results)
		perDir[filepath.Base(dir)] = map[string]any{"total": sum.Total, "kinds": sum.Kinds}
		all = append(all, results...)
	}
	sum := fixturecheck.Summarize(all)
	s.Observe("fixtures.dirs", perDir)
	s.Observe("fixtures.total", sum.Total)
	s.Observe("fixtures.kinds", sum.Kinds)
	s.Observe("fixtures.unknown_fields", sum.Unknown)
	if len(sum.Failed) > 0 {
		s.Finding("fixtures.failed", sum.Failed)
	} else {
		s.Observe("fixtures.failed", sum.Failed)
	}
	return s.Finish(nil)
}

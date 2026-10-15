package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/report"
)

// Report is util report RUN_DIR... (PRU-08): the per-gate Markdown report
// of one or more runs. It reads runs only and writes no record.
type Report struct {
	probe.GlobalOptions
	Out  string `long:"out" description:"Write index.md and one <gate>.md per gate into this directory instead of printing one document."`
	Args struct {
		RunDirs []string `positional-arg-name:"RUN_DIR" description:"Run directories to merge." required:"1"`
	} `positional-args:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *Report) Execute(args []string) error {
	for _, dir := range cmd.Args.RunDirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return exit.New(exit.Usage, fmt.Errorf("util report: %s is not a directory", filepath.Base(dir)))
		}
	}
	ev, err := report.Load(cmd.Args.RunDirs)
	if errors.Is(err, report.ErrNotRun) {
		return exit.New(exit.Usage, fmt.Errorf("util report: %w", err))
	}
	if err != nil {
		return fmt.Errorf("util report: %w", err)
	}
	gates, err := report.Gates()
	if err != nil {
		return fmt.Errorf("util report: %w", err)
	}
	reports := report.Build(ev, gates)
	stdout := cmd.stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	if cmd.Out == "" {
		return report.Render(stdout, ev, reports)
	}
	if err := report.WriteDir(cmd.Out, ev, reports); err != nil {
		return fmt.Errorf("util report: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "%d gates of %d runs: %s\n", len(reports), len(ev.Runs), filepath.Join(cmd.Out, "index.md"))
	return err
}

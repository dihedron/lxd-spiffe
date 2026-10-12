package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// Sanitize is util sanitize DIR (PRU-05).
type Sanitize struct {
	probe.GlobalOptions
	Check bool   `long:"check" description:"Only check; exit 6 if forbidden material is found."`
	Out   string `long:"out" description:"Write the sanitized copy here (default: in place)."`
	Args  struct {
		Dir string `positional-arg-name:"DIR" description:"Evidence directory."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *Sanitize) Execute(args []string) error {
	if cmd.Check && cmd.Out != "" {
		return exit.New(exit.Usage, errors.New("util sanitize: --check and --out are exclusive"))
	}
	if cmd.stdout == nil {
		cmd.stdout = os.Stdout
	}
	report, err := evidence.SanitizeTree(cmd.Args.Dir, cmd.Out, cmd.Check)
	if errors.Is(err, evidence.ErrBadOutput) {
		return exit.New(exit.Usage, fmt.Errorf("util sanitize: %w", err))
	}
	if err != nil {
		return fmt.Errorf("util sanitize: %w", err)
	}
	if err := cmd.print(report); err != nil {
		return err
	}
	switch {
	case cmd.Check && !report.OK():
		return exit.New(exit.Sanitize, fmt.Errorf("util sanitize: %s is not sanitized", cmd.Args.Dir))
	case len(report.Forbidden) > 0:
		return exit.New(exit.Sanitize, fmt.Errorf("util sanitize: %d files cannot be sanitized", len(report.Forbidden)))
	}
	return nil
}

func (cmd *Sanitize) print(report *evidence.TreeReport) error {
	if cmd.JSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("util sanitize: %w", err)
		}
		_, err = fmt.Fprintf(cmd.stdout, "%s\n", data)
		return err
	}
	var b strings.Builder
	verb := "sanitized"
	if cmd.Check {
		verb = "would change"
	}
	fmt.Fprintf(&b, "%d files examined\n", report.Files)
	for _, f := range report.Changed {
		fmt.Fprintf(&b, "%s: %s\n", verb, f)
	}
	for _, f := range report.Forbidden {
		fmt.Fprintf(&b, "forbidden: %s\n", f)
	}
	if !cmd.Check && cmd.Out == "" {
		for _, f := range report.AliasMaps {
			fmt.Fprintf(&b, "kept: %s (the run's alias map; --check fails until it is removed, use --out for a copy to commit)\n", f)
		}
	}
	_, err := io.WriteString(cmd.stdout, b.String())
	return err
}

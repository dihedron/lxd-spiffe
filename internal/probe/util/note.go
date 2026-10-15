package util

import (
	"errors"
	"io"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// Note is util note TEXT (PRU-07): an operator's observation, or with
// --decision the operator's decision for a gate, as a record of the run's
// util environment. The text is sanitized like everything else.
type Note struct {
	probe.GlobalOptions
	Decision bool `long:"decision" description:"Record the operator's decision for the gate."`
	Args     struct {
		Text string `positional-arg-name:"TEXT" description:"Note text."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *Note) Execute(args []string) error {
	if cmd.Decision && cmd.Gate == "" {
		return exit.New(exit.Usage, errors.New("util note: a decision needs --gate"))
	}
	kind, recorded := "observation", []string{}
	if cmd.Decision {
		kind, recorded = "decision", []string{"--decision"}
	}
	s, err := probe.NewSession(&cmd.GlobalOptions, cmd.stdout, "util", "note", recorded)
	if err != nil {
		return err
	}
	s.Observe("note.kind", kind)
	s.Observe("note.text", cmd.Args.Text)
	return s.Finish(nil)
}

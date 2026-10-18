package guest

import (
	"fmt"
	"io"
	"path"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/internal/probe/safety"
	"github.com/dihedron/lxd-spiffe/internal/proof"
)

// RehearseFilePull is guest rehearse file-pull (PRF-24): the agent side of
// file_pull, answering a server rehearsal's challenge.
type RehearseFilePull struct {
	probe.GuestCommand
	Challenge string `long:"challenge" description:"The server rehearsal's challenge line." required:"yes"`
	Tamper    bool   `long:"tamper" description:"Write the proof of another instance name, for the negative rehearsal."`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *RehearseFilePull) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	c, err := rehearse.ParseChallenge(cmd.Challenge)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse file-pull: %w", err))
	}
	if c.Mode != rehearse.FilePull {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse file-pull: a %s challenge", c.Mode))
	}
	if err := safety.Path(c.ProofPath, cmd.AllowSpirePaths); err != nil {
		return err
	}
	serverNonce, agentNonce, err := c.Nonces()
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse file-pull: %w", err))
	}
	name := c.Instance
	if cmd.Tamper {
		name = tamperName(name)
	}
	sum, err := proof.Compute(c.Project, name, serverNonce, agentNonce)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse file-pull: %w", err))
	}
	if cmd.DryRun {
		return dryRun(cmd.stdout, "would write the proof at %s (mode 0600)", c.ProofPath)
	}
	root, rel, spire, err := labRoot(c.ProofPath, cmd.AllowSpirePaths)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read-only handle on the directory
	recArgs := []string{c.ProofPath}
	if cmd.Tamper {
		recArgs = append(recArgs, "--tamper")
	}
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "rehearse file-pull", recArgs)
	if err != nil {
		return err
	}
	s.observe("rehearse.challenge", c)
	err = create(root, rel, proof.Hex(sum), 0o600)
	if err == nil && spire {
		err = create(root, path.Join(path.Dir(rel), markerName(path.Base(rel))), nil, 0o600)
	}
	answer := rehearse.Answer{Mode: rehearse.FilePull, Done: err == nil}
	if err == nil {
		observeStat(s, root, rel, "file.stat")
	} else {
		answer.Error = err.Error()
	}
	s.observe("rehearse.answer", answer)
	if werr := rehearse.WriteAnswer(s.stdout, answer); werr != nil && err == nil {
		err = werr
	}
	return s.finish(err)
}

// tamperName returns another valid instance name of the same length: the
// last character changed, never to a hyphen.
func tamperName(name string) string {
	last := byte('a')
	if name[len(name)-1] == 'a' {
		last = 'b'
	}
	return name[:len(name)-1] + string(last)
}

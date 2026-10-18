package guest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
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

// RehearseConfigPush is guest rehearse config-push (PRF-25): the agent side
// of config_push, reading the challenge key through devLXD.
type RehearseConfigPush struct {
	probe.GuestCommand
	Challenge string `long:"challenge" description:"The server rehearsal's challenge line." required:"yes"`
	Tamper    bool   `long:"tamper" description:"Echo a wrong value, for the negative rehearsal."`

	stdout io.Writer
}

// Execute runs the command. It writes nothing.
func (cmd *RehearseConfigPush) Execute(args []string) error {
	c, err := rehearse.ParseChallenge(cmd.Challenge)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse config-push: %w", err))
	}
	if c.Mode != rehearse.ConfigPush {
		return exit.New(exit.Usage, fmt.Errorf("guest rehearse config-push: a %s challenge", c.Mode))
	}
	recArgs := []string{c.ProofKey}
	if cmd.Tamper {
		recArgs = append(recArgs, "--tamper")
	}
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "rehearse config-push", recArgs)
	if err != nil {
		return err
	}
	s.observe("rehearse.challenge", c)
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cmd.Timeout))
	defer cancel()
	answer := rehearse.Answer{Mode: rehearse.ConfigPush}
	echo, err := readChallengeKey(ctx, s, c.ProofKey)
	switch {
	case err != nil:
		answer.Error = err.Error()
		err = nil // a failed read is the answer, not an error of the probe
	case cmd.Tamper:
		answer.Done, answer.Echo = true, tamperEcho(echo)
	default:
		answer.Done, answer.Echo = true, echo
	}
	s.observe("rehearse.answer", answer)
	if werr := rehearse.WriteAnswer(s.stdout, answer); werr != nil {
		err = werr
	}
	return s.finish(err)
}

// readChallengeKey reads key through devLXD and requires a hex proof
// (FR-A8). The key's value is not redacted in evidence: a lab nonce.
func readChallengeKey(ctx context.Context, s *session, key string) (string, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	resp, err := s.call(ctx, c, "devlxd-config-key", "/1.0/config/"+key, &expect.DevLXDConfigKey)
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", fmt.Errorf("devLXD answered status %d", resp.Status)
	}
	var v string
	if resp.Decode(&v) != nil {
		v = string(resp.Body)
	}
	if !proof.IsHexProof([]byte(v)) {
		return "", fmt.Errorf("the value of %s is not 64 lowercase hex characters", key)
	}
	return v, nil
}

// tamperEcho changes the echo's last character, keeping it hex.
func tamperEcho(echo string) string {
	last := byte('0')
	if echo[len(echo)-1] == '0' {
		last = '1'
	}
	return echo[:len(echo)-1] + string(last)
}

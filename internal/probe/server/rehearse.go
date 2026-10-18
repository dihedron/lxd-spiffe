package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/internal/proof"
)

// filePullSteps are the steps of the file_pull rehearsal, in order
// (PRS-50); --expect-fail names one of them.
var filePullSteps = []string{"resolve", "policy", "challenge", "answer", "read", "file", "compare", "recheck"}

// The backoff of the files API read while a VM's lxd-agent starts (attestor
// spec, Mode file_pull, step 3).
const (
	readBackoff    = 250 * time.Millisecond
	readBackoffMax = 2 * time.Second
)

// rehearsalPolicy are the flags that make the attestor's policy (PRS-50).
type rehearsalPolicy struct {
	AllowPrivileged bool     `long:"allow-privileged" description:"The attestor's policy.allow_privileged."`
	DenyConfigKeys  []string `long:"deny-config-key" description:"A key of the attestor's policy.deny_config_keys; repeatable."`
}

// RehearseFilePull is server rehearse file-pull NAME (PRS-50).
type RehearseFilePull struct {
	probe.ServerCommand
	rehearsalPolicy
	ProofDir   string       `long:"proof-dir" description:"The agent's proof directory." default:"/run/spire/lxd"`
	AgentNonce string       `long:"agent-nonce" description:"Use this agent nonce (64 hex characters) instead of a generated one."`
	ExpectFail string       `long:"expect-fail" description:"The step a negative rehearsal must fail at (PRS-52)."`
	Args       instanceArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
	stdin  io.Reader
}

// rehearsal records the steps of one rehearsal (PRS-52).
type rehearsal struct {
	s          *session
	expectFail string
	failed     string
}

// step records the result of a step and reports whether to go on.
func (r *rehearsal) step(name string, err error) bool {
	if err == nil {
		r.s.observe("step."+name, "ok")
		fmt.Fprintf(r.s.stdout, "step %s: ok\n", name) //nolint:errcheck // the trace is best effort
		return true
	}
	r.failed = name
	r.s.observe("step."+name, err.Error())
	fmt.Fprintf(r.s.stdout, "step %s: FAILED: %v\n", name, err) //nolint:errcheck // the trace is best effort
	if name == r.expectFail {
		r.s.observe("rehearsal.result", "failed at "+name+", as expected")
	} else {
		r.s.finding("rehearsal.result", "failed at "+name)
	}
	return false
}

// conclude records the result of a rehearsal that ran every step.
func (r *rehearsal) conclude() {
	switch {
	case r.failed != "":
	case r.expectFail != "":
		r.s.finding("rehearsal.result", "passed, but was expected to fail at "+r.expectFail)
	default:
		r.s.observe("rehearsal.result", "passed")
	}
}

// Execute runs the command.
func (cmd *RehearseFilePull) Execute(args []string) error {
	if cmd.ExpectFail != "" && !slices.Contains(filePullSteps, cmd.ExpectFail) {
		return exit.New(exit.Usage, fmt.Errorf("server rehearse file-pull: --expect-fail %q: not one of %v", cmd.ExpectFail, filePullSteps))
	}
	if !path.IsAbs(cmd.ProofDir) || path.Clean(cmd.ProofDir) != cmd.ProofDir {
		return exit.New(exit.Usage, fmt.Errorf("server rehearse file-pull: --proof-dir %q is not an absolute clean path", cmd.ProofDir))
	}
	agentNonce, err := agentNonceFlag(cmd.AgentNonce)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("server rehearse file-pull: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	name := cmd.Args.Name
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "rehearse file-pull", rehearsalArgs(name, cmd.ExpectFail, cmd.rehearsalPolicy), name)
	if err != nil {
		return err
	}
	r := &rehearsal{s: s, expectFail: cmd.ExpectFail}
	err = cmd.run(ctx, r, agentNonce)
	if err == nil {
		r.conclude()
	}
	return s.finish(err)
}

// agentNonceFlag decodes --agent-nonce, or generates the nonce the agent
// would.
func agentNonceFlag(flag string) ([]byte, error) {
	if flag == "" {
		return proof.NewNonce()
	}
	b, err := hex.DecodeString(flag)
	if err != nil || len(b) != limits.NonceSize {
		return nil, fmt.Errorf("--agent-nonce: not %d bytes of hex", limits.NonceSize)
	}
	return b, nil
}

func rehearsalArgs(name, expectFail string, p rehearsalPolicy) []string {
	args := []string{name}
	if expectFail != "" {
		args = append(args, "--expect-fail", expectFail)
	}
	if p.AllowPrivileged {
		args = append(args, "--allow-privileged")
	}
	for _, k := range p.DenyConfigKeys {
		args = append(args, "--deny-config-key", k)
	}
	return args
}

// run is the file_pull flow; each step ends the rehearsal when it fails.
// It returns an error only when the probe itself cannot go on.
func (cmd *RehearseFilePull) run(ctx context.Context, r *rehearsal, agentNonce []byte) error {
	s := r.s
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	first, err := rehearseRead(ctx, s, cmd.Args.Name, "instance")
	if err != nil {
		return err
	}
	if !r.step("resolve", first.err) {
		return nil
	}
	inst := first.inst
	decision, err := policy.Check(inst, policy.Policy{
		Projects: []string{cmd.Project}, AllowPrivileged: cmd.AllowPrivileged,
		DenyConfigKeys: cmd.DenyConfigKeys, ProofDir: cmd.ProofDir,
	}, policy.FilePull, "")
	if !r.step("policy", err) {
		return nil
	}
	s.observe("policy.expected_owner", decision.ExpectedOwner)

	serverNonce, err := proof.NewNonce()
	if err != nil {
		return err
	}
	proofPath, err := proof.Path(cmd.ProofDir, agentNonce)
	if err != nil {
		return err
	}
	c := rehearse.Challenge{
		Mode: rehearse.FilePull, Project: inst.Project, Instance: inst.Name,
		ServerNonce: hex.EncodeToString(serverNonce), AgentNonce: hex.EncodeToString(agentNonce),
		ProofPath: proofPath, TimeoutMS: cmd.Timeout.Milliseconds(),
	}
	s.observe("rehearse.challenge", c)
	err = rehearse.WriteChallenge(s.stdout, c)
	if err == nil {
		_, err = fmt.Fprintf(s.stdout, "guest: %s\n", rehearse.GuestCommand(c))
	}
	if !r.step("challenge", err) {
		return nil
	}

	stdin := cmd.stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	answer, err := rehearse.ReadAnswer(ctx, stdin)
	if err == nil {
		s.observe("rehearse.answer", answer)
		switch {
		case answer.Mode != rehearse.FilePull:
			err = fmt.Errorf("answer for mode %q", answer.Mode)
		case !answer.Done:
			err = fmt.Errorf("the guest step failed: %s", answer.Error)
		}
	}
	if !r.step("answer", err) {
		return nil
	}

	resp, err := readProof(ctx, s, cmd.Args.Name, proofPath)
	if err != nil && !errors.Is(err, errNotReady) {
		return err
	}
	if !r.step("read", err) {
		return nil
	}
	if !r.step("file", checkProofFile(resp, decision.ExpectedOwner)) {
		return nil
	}
	want, err := proof.Compute(inst.Project, inst.Name, serverNonce, agentNonce)
	if err != nil {
		return err
	}
	err = nil
	if !proof.Equal(resp.Body, proof.Hex(want)) {
		err = errors.New("the file does not hold the proof")
	}
	if !r.step("compare", err) {
		return nil
	}

	second, err := rehearseRead(ctx, s, cmd.Args.Name, "instance-recheck")
	if err != nil {
		return err
	}
	if second.err == nil {
		second.err = sameInstance(inst, second.inst)
	}
	r.step("recheck", second.err)
	return nil
}

// instanceRead is an instance read: the instance and its ETag, or why it
// is unusable.
type instanceRead struct {
	inst *api.Instance
	etag string
	err  error
}

// rehearseRead reads the instance as the attestor does (one GET, its
// ETag); an answer other than a decodable 200 is a failed step, not an
// error of the probe.
func rehearseRead(ctx context.Context, s *session, name, label string) (instanceRead, error) {
	resp, err := s.call(ctx, label, lxd.RawRequest{Method: http.MethodGet, Path: instancePath(name)}, &expect.Instance)
	if err != nil {
		return instanceRead{}, err
	}
	if resp.Status != http.StatusOK {
		return instanceRead{err: fmt.Errorf("instance read: status %d: %s", resp.Status, errorMessage(resp))}, nil
	}
	var inst api.Instance
	if !s.decode(label, resp, &inst) {
		return instanceRead{err: errors.New("instance read: undecodable answer")}, nil
	}
	return instanceRead{inst: &inst, etag: resp.Header.Get("ETag")}, nil
}

// errorMessage is the message of an error envelope, or the status text.
func errorMessage(resp *lxd.RawResponse) string {
	if env, err := resp.Envelope(); err == nil && env.Error != "" {
		return env.Error
	}
	return http.StatusText(resp.Status)
}

// sameInstance compares the re-read with the first read (FR-S10).
func sameInstance(a, b *api.Instance) error {
	for _, f := range []struct{ name, x, y string }{
		{"status", "Running", b.Status},
		{"volatile.uuid", a.ExpandedConfig["volatile.uuid"], b.ExpandedConfig["volatile.uuid"]},
		{"volatile.uuid.generation", a.ExpandedConfig["volatile.uuid.generation"], b.ExpandedConfig["volatile.uuid.generation"]},
		{"project", a.Project, b.Project},
		{"name", a.Name, b.Name},
		{"location", a.Location, b.Location},
	} {
		if f.x != f.y {
			return fmt.Errorf("%s changed from %q to %q", f.name, f.x, f.y)
		}
	}
	return nil
}

// errNotReady is a files API that never answered within the deadline.
var errNotReady = errors.New("the files API did not answer in time")

// readProof reads the proof file, retrying while a VM's lxd-agent is not
// ready (500, then 404), with a backoff of 250 ms capped at 2 s, until the
// session's deadline. The polls stay out of --record; the answer that ends
// them is read again, recorded.
func readProof(ctx context.Context, s *session, name, proofPath string) (*lxd.RawResponse, error) {
	req := lxd.RawRequest{
		Method: http.MethodGet, Path: "/1.0/instances/" + url.PathEscape(name) + "/files",
		Query: url.Values{"path": {proofPath}}, MaxBytes: limits.MaxFilesResponse,
	}
	s.setQuiet(true)
	attempts, wait := 0, readBackoff
	var last string
	for {
		attempts++
		resp, err := s.client.Raw(ctx, req)
		if err != nil {
			s.setQuiet(false)
			s.observe("read.attempts", attempts)
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%w (last: %s)", errNotReady, last)
			}
			return nil, err
		}
		if resp.Status != http.StatusInternalServerError && resp.Status != http.StatusNotFound {
			break
		}
		last = fmt.Sprintf("status %d: %s", resp.Status, errorMessage(resp))
		select {
		case <-ctx.Done():
			s.setQuiet(false)
			s.observe("read.attempts", attempts)
			return nil, fmt.Errorf("%w (last: %s)", errNotReady, last)
		case <-time.After(wait):
		}
		wait = min(2*wait, readBackoffMax)
	}
	s.setQuiet(false)
	s.observe("read.attempts", attempts)
	resp, err := s.call(ctx, "proof-file", req, &expect.FileRead)
	if err != nil {
		return nil, err
	}
	s.observe("proof-file.headers", lxdHeaders(resp.Header))
	return resp, nil
}

// checkProofFile applies the attestor's checks of the file read (Mode
// file_pull, step 3): a regular file, the expected owner, mode 0600, and
// exactly the size of a hex proof.
func checkProofFile(resp *lxd.RawResponse, owner uint32) error {
	h := resp.Header
	switch {
	case resp.Status != http.StatusOK:
		return fmt.Errorf("status %d: %s", resp.Status, errorMessage(resp))
	case h.Get("X-LXD-type") != "file":
		return fmt.Errorf("type %q, not file", h.Get("X-LXD-type"))
	case h.Get("X-LXD-uid") != strconv.FormatUint(uint64(owner), 10):
		return fmt.Errorf("owner %q, expected %d", h.Get("X-LXD-uid"), owner)
	case h.Get("X-LXD-mode") != "0600":
		return fmt.Errorf("mode %q, not 0600", h.Get("X-LXD-mode"))
	case len(resp.Body) != limits.HexProofSize:
		return fmt.Errorf("%d bytes, not %d", len(resp.Body), limits.HexProofSize)
	}
	return nil
}

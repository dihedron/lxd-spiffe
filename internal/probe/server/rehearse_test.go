package server

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/internal/proof"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const (
	rehUUID = "fbf73246-b07c-4291-afd9-12ae2be70828"
	rehGen  = "7bf31d74-0b1c-4d6e-9a43-5f2e8c1d0a11"
)

// guestSim stands in for the guest half on the server rehearsal's stdin: on
// the first read it takes the challenge from the command's output, acts on
// the fake LXD as the guest would, and returns the answer line.
type guestSim struct {
	t       *testing.T
	fake    *fakelxd.Server
	out     *bytes.Buffer
	act     func(c rehearse.Challenge) rehearse.Answer
	pending *bytes.Reader
}

func (g *guestSim) Read(p []byte) (int, error) {
	if g.pending == nil {
		c, err := rehearse.FindChallenge(g.out.String())
		if err != nil {
			g.t.Fatalf("guest: %v", err)
		}
		var b bytes.Buffer
		_ = rehearse.WriteAnswer(&b, g.act(c))
		g.pending = bytes.NewReader(b.Bytes())
	}
	if g.pending.Len() == 0 {
		return 0, io.EOF
	}
	return g.pending.Read(p)
}

// writeProof is the honest guest of file_pull: it writes the proof of the
// challenge (of another name with tamper) as file f describes it.
func writeProof(fake *fakelxd.Server, tamper bool, f fakelxd.File) func(rehearse.Challenge) rehearse.Answer {
	return func(c rehearse.Challenge) rehearse.Answer {
		sn, an, _ := c.Nonces()
		name := c.Instance
		if tamper {
			name += "x"
		}
		sum, _ := proof.Compute(c.Project, name, sn, an)
		f.Content = string(proof.Hex(sum))
		fake.AddFile(c.Project, c.Instance, c.ProofPath, f)
		return rehearse.Answer{Mode: rehearse.FilePull, Done: true}
	}
}

var goodFile = fakelxd.File{UID: 0, GID: 0, Mode: 0o600, Type: "file"}

func newRehearsalFake(t *testing.T, config map[string]string) *fakelxd.Server {
	fake := fakelxd.NewAssumed(t)
	cfg := map[string]string{"volatile.uuid": rehUUID, "volatile.uuid.generation": rehGen}
	for k, v := range config {
		cfg[k] = v
	}
	fake.AddInstance("default", "c1", cfg)
	return fake
}

func rehearseFilePull(t *testing.T, fake *fakelxd.Server, act func(*fakelxd.Server) func(rehearse.Challenge) rehearse.Answer, mutate func(*RehearseFilePull)) (*RehearseFilePull, evidence.Record, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &RehearseFilePull{ServerCommand: options(t, fake), ProofDir: "/run/spire/lxd", stdout: out}
	cmd.stdin = &guestSim{t: t, fake: fake, out: out, act: act(fake)}
	cmd.Args.Name = "c1"
	if mutate != nil {
		mutate(cmd)
	}
	err := cmd.Execute(nil)
	return cmd, record(t, cmd.ServerCommand, 1), err
}

func honest(fake *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
	return writeProof(fake, false, goodFile)
}

// TestPRS50FilePull: the rehearsal passes every step with an honest guest,
// and the proof never reaches the evidence (PRS-35).
func TestPRS50FilePull(t *testing.T) {
	fake := newRehearsalFake(t, nil)
	cmd, rec, err := rehearseFilePull(t, fake, honest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeOK || observation(t, rec, "rehearsal.result") != "passed" {
		t.Fatalf("outcome %q, observations %v", rec.Outcome, rec.Observations)
	}
	for _, step := range []string{"resolve", "policy", "challenge", "answer", "read", "file", "compare", "recheck"} {
		if got := observation(t, rec, "step."+step); got != "ok" {
			t.Errorf("step %s: %v", step, got)
		}
	}
	c, err := rehearse.FindChallenge(cmd.stdout.(*bytes.Buffer).String())
	if err != nil || c.Instance != "c1" || c.Project != "default" || !strings.HasPrefix(c.ProofPath, "/run/spire/lxd/proof-") {
		t.Errorf("challenge %+v, %v", c, err)
	}
	if !strings.Contains(cmd.stdout.(*bytes.Buffer).String(), "lxd-probe guest rehearse file-pull") {
		t.Error("the guest command is not printed")
	}
	sn, an, _ := c.Nonces()
	sum, _ := proof.Compute("default", "c1", sn, an)
	assertNotInDir(t, cmd.EvidenceDir, string(proof.Hex(sum)))
}

func TestPRS52FilePullFailures(t *testing.T) {
	for name, tt := range map[string]struct {
		config   map[string]string
		act      func(*fakelxd.Server) func(rehearse.Challenge) rehearse.Answer
		mutate   func(*RehearseFilePull)
		failedAt string
		outcome  evidence.Outcome
	}{
		"tampered proof, expected": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer { return writeProof(f, true, goodFile) },
			func(c *RehearseFilePull) { c.ExpectFail = "compare" }, "compare", evidence.OutcomeOK},
		"tampered proof, unexpected": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer { return writeProof(f, true, goodFile) },
			nil, "compare", evidence.OutcomeUnexpected},
		"wrong owner": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
			return writeProof(f, false, fakelxd.File{UID: 1000, Mode: 0o600, Type: "file"})
		}, nil, "file", evidence.OutcomeUnexpected},
		"wrong mode": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
			return writeProof(f, false, fakelxd.File{UID: 0, Mode: 0o644, Type: "file"})
		}, nil, "file", evidence.OutcomeUnexpected},
		"a symlink": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
			return writeProof(f, false, fakelxd.File{UID: 0, Mode: 0o600, Type: "symlink"})
		}, nil, "file", evidence.OutcomeUnexpected},
		"privileged refused by the policy": {map[string]string{"security.privileged": "true"}, honest, nil, "policy", evidence.OutcomeUnexpected},
		"denied config key": {map[string]string{"raw.lxc": "x"}, honest,
			func(c *RehearseFilePull) { c.DenyConfigKeys = []string{"raw.lxc"} }, "policy", evidence.OutcomeUnexpected},
		"the guest failed": {nil, func(*fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
			return func(rehearse.Challenge) rehearse.Answer {
				return rehearse.Answer{Mode: rehearse.FilePull, Error: "permission denied"}
			}
		}, nil, "answer", evidence.OutcomeUnexpected},
		"the instance changed meanwhile": {nil, func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer {
			return func(c rehearse.Challenge) rehearse.Answer {
				f.SetInstanceConfig("default", "c1", "volatile.uuid.generation", "11111111-2222-4333-8444-555555555555")
				return honest(f)(c)
			}
		}, nil, "recheck", evidence.OutcomeUnexpected},
		"expected failure that does not happen": {nil, honest,
			func(c *RehearseFilePull) { c.ExpectFail = "compare" }, "", evidence.OutcomeUnexpected},
	} {
		t.Run(name, func(t *testing.T) {
			_, rec, err := rehearseFilePull(t, newRehearsalFake(t, tt.config), tt.act, tt.mutate)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != tt.outcome {
				t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
			}
			if tt.failedAt != "" {
				if got := observation(t, rec, "step."+tt.failedAt); got == "ok" || got == nil {
					t.Errorf("step %s: %v", tt.failedAt, got)
				}
			}
		})
	}
}

func TestFilePullAllowPrivileged(t *testing.T) {
	_, rec, err := rehearseFilePull(t, newRehearsalFake(t, map[string]string{"security.privileged": "true"}), honest,
		func(c *RehearseFilePull) { c.AllowPrivileged = true })
	if err != nil || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("outcome %q, %v: %v", rec.Outcome, err, rec.Observations)
	}
}

// A VM's files API answers 500 while lxd-agent starts (L7): the read retries.
func TestFilePullVMAgentStarting(t *testing.T) {
	fake := newRehearsalFake(t, nil)
	fake.FailFiles("default", "c1", 2, 500, "LXD VM agent isn't currently running")
	_, rec, err := rehearseFilePull(t, fake, honest, nil)
	if err != nil || rec.Outcome != evidence.OutcomeOK {
		t.Fatalf("outcome %q, %v: %v", rec.Outcome, err, rec.Observations)
	}
	if got := observation(t, rec, "read.attempts"); got != float64(3) {
		t.Errorf("read.attempts %v", got)
	}
}

func TestFilePullStrictAndUsage(t *testing.T) {
	_, _, err := rehearseFilePull(t, newRehearsalFake(t, nil),
		func(f *fakelxd.Server) func(rehearse.Challenge) rehearse.Answer { return writeProof(f, true, goodFile) },
		func(c *RehearseFilePull) { c.Strict = true })
	if exit.Code(err) != exit.Strict {
		t.Errorf("--strict: exit %d (%v)", exit.Code(err), err)
	}
	for name, mutate := range map[string]func(*RehearseFilePull){
		"unknown step":      func(c *RehearseFilePull) { c.ExpectFail = "nonsense" },
		"bad agent nonce":   func(c *RehearseFilePull) { c.AgentNonce = "zz" },
		"relative proofdir": func(c *RehearseFilePull) { c.ProofDir = "run/spire" },
	} {
		cmd := &RehearseFilePull{ServerCommand: options(t, newRehearsalFake(t, nil)), ProofDir: "/run/spire/lxd", stdout: &bytes.Buffer{}}
		cmd.Args.Name = "c1"
		mutate(cmd)
		if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
			t.Errorf("%s: exit %d (%v)", name, exit.Code(err), err)
		}
	}
}

func assertNotInDir(t *testing.T, dir, secret string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if data, _ := os.ReadFile(p); bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s holds the proof", p)
		}
		return nil
	})
}

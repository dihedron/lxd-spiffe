package lxdinstance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spiffe/spire-plugin-sdk/plugintest"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/proof"
	lxdinstancev1 "github.com/dihedron/lxd-spiffe/internal/proto"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

func TestMain(m *testing.M) {
	requiredUID = os.Geteuid()
	os.Exit(m.Run())
}

// harness is the agent plugin served over gRPC as SPIRE would, with the
// test as the SPIRE Agent core relaying a scripted server.
type harness struct {
	t        *testing.T
	proofDir string
	client   *nodeattestorv1.NodeAttestorPluginClient
	log      *syncBuffer
}

// newHarness configures the plugin with hcl plus a proof_dir under a
// temporary directory.
func newHarness(t *testing.T, hcl string) *harness {
	t.Helper()
	h := &harness{t: t, proofDir: filepath.Join(t.TempDir(), "spire", "lxd"), client: new(nodeattestorv1.NodeAttestorPluginClient), log: &syncBuffer{}}
	p, err := configure(fmt.Sprintf("proof_dir = %q\n%s", h.proofDir, hcl))
	if err != nil {
		t.Fatal(err)
	}
	plugintest.ServeInBackground(t, plugintest.Config{
		PluginServer: nodeattestorv1.NodeAttestorPluginServer(p),
		PluginClient: h.client,
		Logger:       hclog.New(&hclog.LoggerOptions{Output: h.log, Level: hclog.Debug}),
	})
	return h
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func configure(hcl string) (*Plugin, error) {
	p := New()
	_, err := p.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: hcl})
	return p, err
}

// session is one attestation seen from the SPIRE Agent core.
type session struct {
	h       *harness
	stream  nodeattestorv1.NodeAttestor_AidAttestationClient
	cancel  context.CancelFunc
	request *lxdinstancev1.AttestRequest
}

// start opens the stream and reads the claim.
func (h *harness) start() *session {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	stream, err := h.client.AidAttestation(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	m, err := stream.Recv()
	if err != nil {
		h.t.Fatalf("reading the payload: %v", err)
	}
	r, err := lxdinstancev1.DecodeAttestRequest(m.GetPayload())
	if err != nil {
		h.t.Fatalf("payload: %v", err)
	}
	return &session{h: h, stream: stream, cancel: cancel, request: r}
}

// challenge is the challenge the server sends for s's request.
func (s *session) challenge() *lxdinstancev1.Challenge {
	s.h.t.Helper()
	nonce, _ := proof.NewNonce()
	p, err := proof.Path(s.h.proofDir, s.request.GetAgentNonce())
	if err != nil {
		s.h.t.Fatal(err)
	}
	return &lxdinstancev1.Challenge{Version: 1, Mode: lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL, ServerNonce: nonce, ProofPath: p, TimeoutMs: 10000}
}

// send sends c and returns the agent's answer.
func (s *session) send(c *lxdinstancev1.Challenge) (*lxdinstancev1.ChallengeResponse, error) {
	data, err := proto.Marshal(c)
	if err != nil {
		s.h.t.Fatal(err)
	}
	if err := s.stream.Send(&nodeattestorv1.Challenge{Challenge: data}); err != nil {
		s.h.t.Fatal(err)
	}
	m, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	return lxdinstancev1.DecodeChallengeResponse(m.GetChallengeResponse())
}

// rightProof is the proof the server expects for c.
func (s *session) rightProof(c *lxdinstancev1.Challenge) string {
	sum, err := proof.Compute(s.request.GetClaim().GetProject(), s.request.GetClaim().GetInstanceName(), c.GetServerNonce(), s.request.GetAgentNonce())
	if err != nil {
		s.h.t.Fatal(err)
	}
	return string(proof.Hex(sum))
}

// gone waits for path to disappear.
func gone(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
	}
	t.Errorf("%s still exists", path)
}

// TestFRA1FilePull runs a file_pull attestation: the payload carries the
// devLXD claim, the proof file holds the right proof until the SPIRE Agent
// closes the stream, then it is removed.
func TestFRA1FilePull(t *testing.T) {
	dev := fakelxd.NewDevLXD(t)
	h := newHarness(t, fmt.Sprintf("devlxd_socket = %q\nclaim { project = \"spire-nodes\" }", dev.Socket))
	s := h.start()
	r := s.request
	switch {
	case r.GetVersion() != 1,
		r.GetClaim().GetProject() != "spire-nodes",
		r.GetClaim().GetInstanceName() != "c1",
		r.GetClaim().GetNameSource() != lxdinstancev1.ClaimSource_CLAIM_SOURCE_DEVLXD_METADATA,
		r.GetClaim().GetCloudInitInstanceId() != "7c64d5b4-63d7-4bd6-9a5e-0d3b5a1c2f10",
		r.GetInstanceType() != lxdinstancev1.InstanceType_INSTANCE_TYPE_CONTAINER,
		!slices.Equal(r.GetProofModes(), []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL, lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH}),
		r.GetPluginVersion() == "":
		t.Fatalf("payload %v", r)
	}
	c := s.challenge()
	resp, err := s.send(c)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetDone() || resp.GetMode() != lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL || len(resp.GetEcho()) != 0 {
		t.Fatalf("response %v", resp)
	}
	got, err := os.ReadFile(c.GetProofPath())
	if err != nil || string(got) != s.rightProof(c) {
		t.Fatalf("proof file %q, %v; want the right proof", got, err)
	}
	if fi, _ := os.Lstat(c.GetProofPath()); fi.Mode() != 0o600 {
		t.Errorf("proof file mode %v", fi.Mode())
	}
	s.cancel()
	gone(t, c.GetProofPath())
	checkSecretsNotLogged(t, h.log.String(), r.GetAgentNonce(), c.GetServerNonce(), []byte(s.rightProof(c)))
}

// checkSecretsNotLogged: FR-A10, no nonce or proof in the logs.
func checkSecretsNotLogged(t *testing.T, log string, nonces ...[]byte) {
	t.Helper()
	if !strings.Contains(log, "attesting") {
		t.Fatalf("no attestation log: %s", log)
	}
	for _, n := range nonces {
		for _, form := range []string{hex.EncodeToString(n), base64.StdEncoding.EncodeToString(n), string(n)} {
			if len(form) >= 16 && strings.Contains(log, form) {
				t.Errorf("FR-A10: log contains %q", form)
			}
		}
	}
}

// TestFRA2ClaimSources checks the claim's precedence: configuration, then
// devLXD meta-data, then the hostname.
func TestFRA2ClaimSources(t *testing.T) {
	dev := fakelxd.NewDevLXD(t)
	hostname, _ := os.Hostname()
	cases := map[string]struct {
		hcl    string
		name   string
		source lxdinstancev1.ClaimSource
		typ    lxdinstancev1.InstanceType
	}{
		"configured name": {fmt.Sprintf("devlxd_socket = %q\nclaim { instance_name = \"n1\" }", dev.Socket), "n1", lxdinstancev1.ClaimSource_CLAIM_SOURCE_CONFIGURED, lxdinstancev1.InstanceType_INSTANCE_TYPE_CONTAINER},
		"devLXD off":      {`use_devlxd = "off"`, hostname, lxdinstancev1.ClaimSource_CLAIM_SOURCE_HOSTNAME, lxdinstancev1.InstanceType_INSTANCE_TYPE_UNSPECIFIED},
		"devLXD absent":   {`devlxd_socket = "/nonexistent/sock"`, hostname, lxdinstancev1.ClaimSource_CLAIM_SOURCE_HOSTNAME, lxdinstancev1.InstanceType_INSTANCE_TYPE_UNSPECIFIED},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			r := newHarness(t, c.hcl).start().request
			if r.GetClaim().GetInstanceName() != c.name || r.GetClaim().GetNameSource() != c.source || r.GetInstanceType() != c.typ {
				t.Errorf("claim %v, type %v; want %s from %v, %v", r.GetClaim(), r.GetInstanceType(), c.name, c.source, c.typ)
			}
		})
	}
}

// TestFRA3DevLXDEnvelope reads devLXD over vsock, in the LXD envelope.
func TestFRA3DevLXDEnvelope(t *testing.T) {
	dev := fakelxd.NewDevLXD(t)
	dev.Envelope, dev.InstanceType = true, "virtual-machine"
	r := newHarness(t, fmt.Sprintf("use_devlxd = \"required\"\ndevlxd_socket = %q", dev.Socket)).start().request
	if r.GetClaim().GetInstanceName() != "c1" || r.GetInstanceType() != lxdinstancev1.InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE {
		t.Errorf("claim %v, type %v", r.GetClaim(), r.GetInstanceType())
	}
}

// TestFRA3DevLXDRequired: without devLXD, use_devlxd = "required" fails the
// attestation before anything is sent.
func TestFRA3DevLXDRequired(t *testing.T) {
	h := newHarness(t, `use_devlxd = "required"
devlxd_socket = "/nonexistent/sock"`)
	stream, err := h.client.AidAttestation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "devLXD is required") {
		t.Errorf("Recv = %v; want Internal, devLXD is required", err)
	}
}

// TestFRA6Refusals: a challenge the agent cannot answer ends the
// attestation with an error and leaves no proof file.
func TestFRA6Refusals(t *testing.T) {
	cases := map[string]func(s *session, c *lxdinstancev1.Challenge){
		"config_push": func(_ *session, c *lxdinstancev1.Challenge) {
			c.Mode, c.ServerNonce, c.ProofPath, c.ProofKey = lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH, nil, "", "user.spire.challenge.0123456789abcdef"
		},
		"version 2":           func(_ *session, c *lxdinstancev1.Challenge) { c.Version = 2 },
		"short server nonce":  func(_ *session, c *lxdinstancev1.Challenge) { c.ServerNonce = c.ServerNonce[:31] },
		"path in another dir": func(s *session, c *lxdinstancev1.Challenge) { c.ProofPath = "/tmp/" + filepath.Base(c.ProofPath) },
		"path of another nonce": func(s *session, c *lxdinstancev1.Challenge) {
			c.ProofPath = filepath.Join(s.h.proofDir, "proof-0123456789abcdef")
		},
		"path with a dot dot": func(s *session, c *lxdinstancev1.Challenge) {
			c.ProofPath = filepath.Join(s.h.proofDir, "x") + "/../" + filepath.Base(c.ProofPath)
		},
		"proof dir world-readable": func(s *session, c *lxdinstancev1.Challenge) {
			if err := os.MkdirAll(s.h.proofDir, 0o755); err != nil {
				s.h.t.Fatal(err)
			}
			if err := os.Chmod(s.h.proofDir, 0o755); err != nil {
				s.h.t.Fatal(err)
			}
		},
	}
	for label, change := range cases {
		t.Run(label, func(t *testing.T) {
			s := newHarness(t, `use_devlxd = "off"`).start()
			c := s.challenge()
			path := c.GetProofPath()
			change(s, c)
			if _, err := s.send(c); status.Code(err) != codes.Internal {
				t.Fatalf("answer = %v; want Internal", err)
			} else {
				t.Log(err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("proof file left: %v", err)
			}
		})
	}
}

// TestFRA11AttestationTimeout: with no challenge, the attestation fails at
// the configured attestation_timeout.
func TestFRA11AttestationTimeout(t *testing.T) {
	h := newHarness(t, `use_devlxd = "off"
proof_timeout = "1s"
attestation_timeout = "2s"`)
	s := h.start()
	start := time.Now()
	_, err := s.stream.Recv()
	if elapsed := time.Since(start); status.Code(err) != codes.DeadlineExceeded || !strings.Contains(err.Error(), "attestation_timeout of 2s") || elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
		t.Errorf("Recv = %v after %v; want DeadlineExceeded for attestation_timeout of 2s", err, elapsed)
	}
}

// TestFRA11ProofTimeout: a server that never ends the stream gets the
// proof for proof_timeout, then the file is removed.
func TestFRA11ProofTimeout(t *testing.T) {
	s := newHarness(t, `use_devlxd = "off"
proof_timeout = "1s"
attestation_timeout = "10s"`).start()
	c := s.challenge()
	start := time.Now()
	if _, err := s.send(c); err != nil {
		t.Fatal(err)
	}
	_, err := s.stream.Recv()
	if elapsed := time.Since(start); status.Code(err) != codes.DeadlineExceeded || !strings.Contains(err.Error(), "proof_timeout of 1s") || elapsed > 3*time.Second {
		t.Errorf("Recv = %v after %v; want DeadlineExceeded for proof_timeout of 1s", err, elapsed)
	}
	gone(t, c.GetProofPath())
}

// TestFRA7NotRoot: Configure refuses an agent that does not run as root.
func TestFRA7NotRoot(t *testing.T) {
	defer func(uid int) { requiredUID = uid }(requiredUID)
	requiredUID = os.Geteuid() + 1
	if _, err := configure(""); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "must run as uid") {
		t.Errorf("Configure = %v; want FailedPrecondition", err)
	}
}

func TestConfigureDefaults(t *testing.T) {
	p, err := configure("")
	if err != nil {
		t.Fatal(err)
	}
	s := p.current()
	if s.devLXD != "auto" || s.socket != "/dev/lxd/sock" || s.proofDir != "/run/spire/lxd" || s.proofTimeout != 15*time.Second || s.attestationTimeout != 30*time.Second || s.claim.DevLXDRequired {
		t.Errorf("settings %+v", s)
	}
	if p, err = configure("attestation_timeout = \"2m\"\nproof_timeout = \"90s\""); err != nil || p.current().attestationTimeout != 2*time.Minute || p.current().proofTimeout != 90*time.Second {
		t.Errorf("configured timeouts: %v", err)
	}
}

func TestConfigureRefusals(t *testing.T) {
	for _, hcl := range []string{
		`unknown = "x"`,
		`use_devlxd = "yes"`,
		`proof_dir = "run/spire"`,
		`proof_dir = "/"`,
		`claim { project = "a/b" }`,
		`claim { instance_name = "a_b" }`,
		`proof_timeout = "500ms"`,
		`proof_timeout = "3m"`,
		`attestation_timeout = "1s"`,
		`attestation_timeout = "6m"`,
		`attestation_timeout = "15s"`, // not longer than the default proof_timeout
		`attestation_timeout = "x"`,
	} {
		if _, err := configure(hcl); status.Code(err) != codes.InvalidArgument {
			t.Errorf("Configure(%s) = %v; want InvalidArgument", hcl, err)
		} else {
			t.Log(err)
		}
	}
}

func TestAidAttestationNotConfigured(t *testing.T) {
	if err := New().AidAttestation(nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("AidAttestation before Configure = %v; want FailedPrecondition", err)
	}
}

// TestFRA5ProofModes: config_push is offered only when devLXD answers.
func TestFRA5ProofModes(t *testing.T) {
	dev := fakelxd.NewDevLXD(t)
	pull := []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL}
	both := append(slices.Clone(pull), lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH)
	for hcl, want := range map[string][]lxdinstancev1.ProofMode{
		fmt.Sprintf("devlxd_socket = %q", dev.Socket): both,
		`use_devlxd = "off"`:                          pull,
		`devlxd_socket = "/nonexistent/sock"`:         pull,
	} {
		if got := newHarness(t, hcl).start().request.GetProofModes(); !slices.Equal(got, want) {
			t.Errorf("%s: modes %v, want %v", hcl, got, want)
		}
	}
}

// pushChallenge is the config_push challenge for s, with the key of s's
// nonce.
func (s *session) pushChallenge() *lxdinstancev1.Challenge {
	s.h.t.Helper()
	key, err := proof.Key(s.request.GetAgentNonce())
	if err != nil {
		s.h.t.Fatal(err)
	}
	return &lxdinstancev1.Challenge{Version: 1, Mode: lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH, ProofKey: key, TimeoutMs: 10000}
}

// TestFRA8ConfigPush: the agent echoes the challenge key's value read
// through devLXD, plain or in an envelope.
func TestFRA8ConfigPush(t *testing.T) {
	for _, envelope := range []bool{false, true} {
		dev := fakelxd.NewDevLXD(t)
		dev.Envelope = envelope
		s := newHarness(t, fmt.Sprintf("devlxd_socket = %q", dev.Socket)).start()
		c := s.pushChallenge()
		value := strings.Repeat("ab", 32)
		dev.SetConfig(c.GetProofKey(), value)
		resp, err := s.send(c)
		if err != nil {
			t.Fatalf("envelope %v: %v", envelope, err)
		}
		if !resp.GetDone() || resp.GetMode() != lxdinstancev1.ProofMode_PROOF_MODE_CONFIG_PUSH || string(resp.GetEcho()) != value {
			t.Errorf("envelope %v: response %v", envelope, resp)
		}
		checkSecretsNotLogged(t, s.h.log.String(), s.request.GetAgentNonce(), []byte(value))
	}
}

// TestFRA8Refusals: a key not derived from the agent's nonce, a missing
// key or a value that is not a proof end the attestation with an error.
func TestFRA8Refusals(t *testing.T) {
	cases := map[string]func(dev *fakelxd.DevLXD, c *lxdinstancev1.Challenge){
		"key of another nonce": func(dev *fakelxd.DevLXD, c *lxdinstancev1.Challenge) {
			c.ProofKey = "user.spire.challenge.0123456789abcdef"
			dev.SetConfig(c.ProofKey, strings.Repeat("ab", 32))
		},
		"missing key": func(*fakelxd.DevLXD, *lxdinstancev1.Challenge) {},
		"short value": func(dev *fakelxd.DevLXD, c *lxdinstancev1.Challenge) {
			dev.SetConfig(c.GetProofKey(), strings.Repeat("ab", 31))
		},
		"upper-case hex": func(dev *fakelxd.DevLXD, c *lxdinstancev1.Challenge) {
			dev.SetConfig(c.GetProofKey(), strings.Repeat("AB", 32))
		},
	}
	for label, change := range cases {
		t.Run(label, func(t *testing.T) {
			dev := fakelxd.NewDevLXD(t)
			s := newHarness(t, fmt.Sprintf("devlxd_socket = %q", dev.Socket)).start()
			c := s.pushChallenge()
			change(dev, c)
			if _, err := s.send(c); status.Code(err) != codes.Internal {
				t.Fatalf("answer = %v; want Internal", err)
			} else {
				t.Log(err)
			}
		})
	}
}

// TestFRA3DevLXDServerUnreadable: under auto, a devLXD whose GET /1.0 is
// unusable still gives the claim its meta-data, but config_push is not
// offered and its challenge is refused.
func TestFRA3DevLXDServerUnreadable(t *testing.T) {
	dev := fakelxd.NewDevLXD(t)
	dev.InstanceType = "unknown"
	s := newHarness(t, fmt.Sprintf("devlxd_socket = %q", dev.Socket)).start()
	r := s.request
	if r.GetClaim().GetNameSource() != lxdinstancev1.ClaimSource_CLAIM_SOURCE_DEVLXD_METADATA || !slices.Equal(r.GetProofModes(), []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL}) {
		t.Fatalf("claim %v, modes %v", r.GetClaim(), r.GetProofModes())
	}
	if _, err := s.send(s.pushChallenge()); status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "does not offer") {
		t.Errorf("config_push challenge: %v; want refused", err)
	}
}

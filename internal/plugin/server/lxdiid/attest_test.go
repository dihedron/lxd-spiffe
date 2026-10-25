package lxdiid

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/spiffe/spire-plugin-sdk/plugintest"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/proof"
	lxdiidv1 "github.com/dihedron/lxd-spiffe/internal/proto"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const (
	uuidA = "fbf73246-b07c-4291-afd9-12ae2be70828"
	uuidB = "0b0c5a7e-6a0e-4d8e-9a3b-2c1f6e4d7a91"
)

// harness is the server plugin, configured against one fake LXD (server
// "lab", projects default and other), served over gRPC as SPIRE would.
type harness struct {
	t      *testing.T
	fake   *fakelxd.Server
	client *nodeattestorv1.NodeAttestorPluginClient
	log    *syncBuffer
	plugin *Plugin
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

// newHarness configures the plugin with the top-level lines top and, when
// given, extra server entries (HCL) besides "lab".
func newHarness(t *testing.T, top string, extraServers ...string) *harness {
	t.Helper()
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions(lxd.RequiredExtensions...)
	f := newTestFiles(t)
	servers := fmt.Sprintf(`"lab" = {
    url = %q
    client_cert_file = %q
    client_key_file = %q
    server_cert_fingerprint = %q
    projects = ["default", "other"]
  }`, fake.URL, f.cert, f.key, fake.Fingerprint())
	hcl := top + "\nservers = {\n  " + strings.Join(append([]string{servers}, extraServers...), "\n  ") + "\n}\n"
	p, err := configure(t, hcl)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: fake, client: new(nodeattestorv1.NodeAttestorPluginClient), log: &syncBuffer{}, plugin: p}
	plugintest.ServeInBackground(t, plugintest.Config{
		PluginServer: nodeattestorv1.NodeAttestorPluginServer(p),
		PluginClient: h.client,
		Logger:       hclog.New(&hclog.LoggerOptions{Output: h.log, Level: hclog.Debug}),
	})
	return h
}

// addInstance adds a running instance with a volatile.uuid.
func (h *harness) addInstance(project, name, typ, uuid string) {
	h.fake.AddInstance(project, name, map[string]string{"volatile.uuid": uuid, "volatile.uuid.generation": uuid})
	h.fake.SetType(project, name, typ)
}

// agent is a scripted SPIRE agent.
type agent struct {
	project, name, cloudInitID string
	typ                        lxdiidv1.InstanceType
	modes                      []lxdiidv1.ProofMode
	// answer handles the challenge; nil writes the right proof, as root,
	// into the fake for project/name.
	answer func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse
	// payload, when set, is sent instead of a well-formed request.
	payload    []byte
	nonce      []byte
	challenged bool
}

func newAgent(name string) *agent {
	nonce, _ := proof.NewNonce()
	return &agent{name: name, nonce: nonce, modes: []lxdiidv1.ProofMode{lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL}}
}

// writeProof stores the proof file of challenge c in the fake, as the
// agent would write it, with the given owner, mode and content.
func (h *harness) writeProof(project string, a *agent, c *lxdiidv1.Challenge, uid, mode int, content string) {
	h.fake.AddFile(project, a.name, c.GetProofPath(), fakelxd.File{Content: content, UID: uid, Mode: mode, Type: "file"})
	h.fake.AddFile(project, a.name, defaultProofDir, fakelxd.File{UID: 0, Mode: 0o700, Type: "directory", Entries: []string{}})
}

func (a *agent) rightProof(c *lxdiidv1.Challenge) string {
	sum, err := proof.Compute(a.project, a.name, c.GetServerNonce(), a.nonce)
	if err != nil {
		panic(err)
	}
	return string(proof.Hex(sum))
}

var done = &lxdiidv1.ChallengeResponse{Version: 1, Mode: lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL, Done: true}

// attest runs one attestation of a against the instance in project
// (where the default answer writes the proof).
func (h *harness) attest(project string, a *agent) (*nodeattestorv1.AgentAttributes, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	stream, err := h.client.Attest(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := stream.Send(&nodeattestorv1.AttestRequest{Request: &nodeattestorv1.AttestRequest_Payload{Payload: a.request(h.t)}}); err != nil {
		h.t.Fatal(err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if attrs := resp.GetAgentAttributes(); attrs != nil {
			return attrs, nil
		}
		r := h.answer(project, a, resp.GetChallenge())
		if r == nil {
			// the agent never answers: wait for the server to give up
			_, err := stream.Recv()
			return nil, err
		}
		if err := stream.Send(&nodeattestorv1.AttestRequest{Request: &nodeattestorv1.AttestRequest_ChallengeResponse{ChallengeResponse: marshal(h.t, r)}}); err != nil {
			h.t.Fatal(err)
		}
	}
}

// request is a's payload: a well-formed request unless a.payload is set.
func (a *agent) request(t *testing.T) []byte {
	if a.payload != nil {
		return a.payload
	}
	return marshal(t, &lxdiidv1.AttestRequest{
		Version:      1,
		Claim:        &lxdiidv1.Claim{Project: a.project, InstanceName: a.name, CloudInitInstanceId: a.cloudInitID},
		AgentNonce:   a.nonce,
		InstanceType: a.typ,
		ProofModes:   a.modes,
	})
}

// answer is a's answer to the challenge in data: a.answer's, or the right
// proof written as root.
func (h *harness) answer(project string, a *agent, data []byte) *lxdiidv1.ChallengeResponse {
	h.t.Helper()
	a.challenged = true
	c, err := lxdiidv1.DecodeChallenge(data)
	if err != nil {
		h.t.Fatalf("the server sent an invalid challenge: %v", err)
	}
	if a.answer != nil {
		return a.answer(h, a, c)
	}
	h.writeProof(project, a, c, 0, 0o600, a.rightProof(c))
	return done
}

func marshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFRS1FilePullContainer: a container proves co-location with the proof
// file and gets its SPIFFE ID, selectors and can_reattest (FR-S6–S9).
func TestFRS1FilePullContainer(t *testing.T) {
	h := newHarness(t, "")
	h.addInstance("default", "c1", "container", uuidA)
	attrs, err := h.attest("default", newAgent("c1"))
	if err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	if want := "spiffe://example.org/spire/agent/lxd_iid/lab/" + uuidA; attrs.GetSpiffeId() != want {
		t.Errorf("FR-S7 SPIFFE ID %q, want %q", attrs.GetSpiffeId(), want)
	}
	for _, sel := range []string{"server:lab", "uuid:" + uuidA, "project:default", "type:container"} {
		if !contains(attrs.GetSelectorValues(), sel) {
			t.Errorf("FR-S8 selector %q missing from %v", sel, attrs.GetSelectorValues())
		}
	}
	if !attrs.GetCanReattest() {
		t.Error("FR-S9 can_reattest false with reattest allow")
	}
	if log := h.log.String(); !strings.Contains(log, "reason_code=ok") || !strings.Contains(log, "correlation_id=") {
		t.Errorf("no audit line with the correlation ID:\n%s", log)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// TestL7FilePullVMWaitsForAgent: a VM whose lxd-agent is still starting
// answers 500 and then 404 for a while; the server retries within
// proof_timeout.
func TestL7FilePullVMWaitsForAgent(t *testing.T) {
	h := newHarness(t, `proof_timeout = "10s"`)
	h.addInstance("default", "v1", "virtual-machine", uuidA)
	h.fake.FailFiles("default", "v1", 2, 500, "LXD VM agent is not currently running")
	a := newAgent("v1")
	a.typ = lxdiidv1.InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE
	attrs, err := h.attest("default", a)
	if err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	if !contains(attrs.GetSelectorValues(), "type:virtual-machine") {
		t.Errorf("selectors %v", attrs.GetSelectorValues())
	}
}

// TestFRS9ReattestDenyAndTemplate: reattest deny, and the agent path from
// agent_path_template (FR-S7).
func TestFRS9ReattestDenyAndTemplate(t *testing.T) {
	h := newHarness(t, "reattest = \"deny\"\nagent_path_template = \"lxd/{{ .ServerID }}/node/{{ .InstanceUUID }}\"")
	h.addInstance("other", "c1", "container", uuidA)
	attrs, err := h.attest("other", newAgent("c1"))
	if err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	if attrs.GetCanReattest() || attrs.GetSpiffeId() != "spiffe://example.org/spire/agent/lxd/lab/node/"+uuidA {
		t.Errorf("can_reattest %v, SPIFFE ID %q", attrs.GetCanReattest(), attrs.GetSpiffeId())
	}
}

// TestSEC02ClaimOnly: proof_mode none issues on the claim, sends no
// challenge, and warns on every attestation.
func TestSEC02ClaimOnly(t *testing.T) {
	h := newHarness(t, "proof_mode = \"none\"\ni_understand_claim_only_is_insecure = true")
	h.addInstance("default", "c1", "container", uuidA)
	a := newAgent("c1")
	if _, err := h.attest("default", a); err != nil {
		t.Fatalf("attestation failed: %v\n%s", err, h.log)
	}
	if a.challenged {
		t.Error("a challenge was sent in claim-only mode")
	}
	if !strings.Contains(h.log.String(), "claim only") {
		t.Errorf("no warning:\n%s", h.log)
	}
}

// refusal is an attestation that must fail with the uniform error and the
// reason code in the server's log.
type refusal struct {
	top    string
	setup  func(h *harness, a *agent)
	reason string
}

// TestSEC10Refusals: every failure answers the agent with the same
// PermissionDenied "attestation failed" (SEC-10); the server's log names
// the reason.
func TestSEC10Refusals(t *testing.T) {
	wrong := func(uid, mode int, content func(a *agent, c *lxdiidv1.Challenge) string, typ string) func(*harness, *agent) {
		return func(_ *harness, a *agent) {
			a.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				h.fake.AddFile("default", a.name, c.GetProofPath(), fakelxd.File{Content: content(a, c), UID: uid, Mode: mode, Type: typ})
				h.fake.AddFile("default", a.name, defaultProofDir, fakelxd.File{Mode: 0o700, Type: "directory", Entries: []string{}})
				return done
			}
		}
	}
	right := func(a *agent, c *lxdiidv1.Challenge) string { return a.rightProof(c) }
	cases := map[string]refusal{
		"FR-S2 bad payload": {setup: func(_ *harness, a *agent) { a.payload = []byte{0xff, 0x01} }, reason: "payload_invalid"},
		"FR-S2 no file_pull offered": {setup: func(_ *harness, a *agent) {
			a.modes = []lxdiidv1.ProofMode{lxdiidv1.ProofMode_PROOF_MODE_CONFIG_PUSH}
		}, reason: "policy_denied"},
		"FR-S3 unknown instance":      {setup: func(_ *harness, a *agent) { a.name = "nobody" }, reason: "instance_not_found"},
		"FR-S3 project not allowed":   {setup: func(_ *harness, a *agent) { a.project = "elsewhere" }, reason: "scope_denied"},
		"FR-S3 cloud-init id differs": {setup: func(_ *harness, a *agent) { a.cloudInitID = uuidB }, reason: "instance_not_found"},
		"SEC-05 same name in two projects": {setup: func(h *harness, _ *agent) {
			h.addInstance("other", "c1", "container", uuidB)
		}, reason: "instance_ambiguous"},
		"FR-S18 twin with the same uuid": {setup: func(h *harness, _ *agent) {
			h.addInstance("other", "twin", "container", uuidA)
		}, reason: "instance_ambiguous"},
		"FR-S5 stopped": {setup: func(h *harness, _ *agent) { h.fake.SetStatus("default", "c1", "Stopped") }, reason: "not_running"},
		"FR-S5 privileged": {setup: func(h *harness, _ *agent) {
			h.fake.SetInstanceConfig("default", "c1", "security.privileged", "true")
		}, reason: "policy_denied"},
		"FR-S5 type hint differs": {setup: func(_ *harness, a *agent) { a.typ = lxdiidv1.InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE }, reason: "policy_denied"},
		"T2 disk over the proof directory": {setup: func(h *harness, _ *agent) {
			h.fake.SetDevice("default", "c1", "shared", map[string]string{"type": "disk", "path": "/run", "source": "/srv/x"})
		}, reason: "policy_denied"},
		"proof content wrong":      {setup: wrong(0, 0o600, func(*agent, *lxdiidv1.Challenge) string { return strings.Repeat("0", 64) }, "file"), reason: "proof_mismatch"},
		"proof short":              {setup: wrong(0, 0o600, func(a *agent, c *lxdiidv1.Challenge) string { return a.rightProof(c)[:63] }, "file"), reason: "proof_mismatch"},
		"proof not owned by root":  {setup: wrong(1000, 0o600, right, "file"), reason: "proof_mismatch"},
		"proof readable by others": {setup: wrong(0, 0o644, right, "file"), reason: "proof_mismatch"},
		"proof with a setuid bit":  {setup: wrong(0, 0o4600, right, "file"), reason: "proof_mismatch"},
		"proof is a symlink":       {setup: wrong(0, 0o600, right, "symlink"), reason: "proof_mismatch"},
		"proof file missing": {setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse { return done }
		}, reason: "proof_mismatch"},
		"proof directory open to others": {setup: func(_ *harness, a *agent) {
			a.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				h.writeProof("default", a, c, 0, 0o600, a.rightProof(c))
				h.fake.AddFile("default", a.name, defaultProofDir, fakelxd.File{Mode: 0o755, Type: "directory", Entries: []string{}})
				return done
			}
		}, reason: "proof_mismatch"},
		"guest step failed": {setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				return &lxdiidv1.ChallengeResponse{Version: 1, Mode: lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL}
			}
		}, reason: "proof_mismatch"},
		"SEC-03 agent never answers": {top: `proof_timeout = "1s"`, setup: func(_ *harness, a *agent) {
			a.answer = func(*harness, *agent, *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse { return nil }
		}, reason: "proof_timeout"},
		"L7 VM agent never up": {top: `proof_timeout = "1s"`, setup: func(h *harness, a *agent) {
			h.fake.SetType("default", "c1", "virtual-machine")
			h.fake.FailFiles("default", "c1", 1000, 500, "LXD VM agent is not currently running")
		}, reason: "vm_agent_unavailable"},
		"E2E-3 no files grant": {setup: func(h *harness, _ *agent) {
			h.fake.FailFiles("default", "c1", 1000, 403, "not authorized")
		}, reason: "lxd_forbidden"},
		"files API failing": {setup: func(h *harness, _ *agent) {
			h.fake.FailFiles("default", "c1", 1000, 500, "database is not available")
		}, reason: "lxd_unavailable"},
		"no instance grant": {setup: func(h *harness, _ *agent) {
			h.fake.Deny("/1.0/instances")
		}, reason: "lxd_forbidden"},
		"FR-S10 moved during the proof": {setup: func(_ *harness, a *agent) {
			a.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				h.writeProof("default", a, c, 0, 0o600, a.rightProof(c))
				h.fake.SetLocation("default", a.name, "member2")
				return done
			}
		}, reason: "instance_moved"},
		"FR-S10 stopped during the proof": {setup: func(_ *harness, a *agent) {
			a.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				h.writeProof("default", a, c, 0, 0o600, a.rightProof(c))
				h.fake.SetStatus("default", a.name, "Stopped")
				return done
			}
		}, reason: "not_running"},
		"FR-S10 replaced during the proof": {setup: func(_ *harness, a *agent) {
			a.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
				h.writeProof("default", a, c, 0, 0o600, a.rightProof(c))
				h.fake.SetInstanceConfig("default", a.name, "volatile.uuid", uuidB)
				return done
			}
		}, reason: "instance_not_found"},
		"L16 network project unreadable": {setup: func(h *harness, _ *agent) {
			h.fake.SetDevice("default", "c1", "eth0", map[string]string{"type": "nic", "network": "lxdbr0"})
		}, reason: "project_unreadable"},
		"FR-S12 server lacks an extension": {setup: func(h *harness, _ *agent) {
			h.fake.SetExtensions("projects", "instance_generation_id")
		}, reason: "extension_missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, c.top)
			h.addInstance("default", "c1", "container", uuidA)
			a := newAgent("c1")
			c.setup(h, a)
			_, err := h.attest("default", a)
			checkRefused(t, h, err, c.reason)
		})
	}
}

// TestFRS3UnreachableServerFailsClosed: with a server down, uniqueness
// cannot be proven, so the attestation fails even where the instance is
// found.
func TestFRS3UnreachableServerFailsClosed(t *testing.T) {
	f := newTestFiles(t)
	down := fmt.Sprintf(`"down" = {
    url = "https://127.0.0.1:1"
    client_cert_file = %q
    client_key_file = %q
    server_cert_fingerprint = %q
    projects = ["default"]
  }`, f.cert, f.key, testPin)
	h := newHarness(t, "", down)
	h.addInstance("default", "c1", "container", uuidA)
	_, err := h.attest("default", newAgent("c1"))
	checkRefused(t, h, err, "lxd_unavailable")
}

func checkRefused(t *testing.T, h *harness, err error, reason string) {
	t.Helper()
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "attestation failed" {
		t.Fatalf("SEC-10: %v, want PermissionDenied: attestation failed", err)
	}
	// one attestation, one reason: the expected one
	if log := h.log.String(); strings.Count(log, "reason_code=") != 1 || !strings.Contains(log, "reason_code="+reason+" ") && !strings.HasSuffix(strings.TrimSpace(log), "reason_code="+reason) {
		t.Errorf("want one reason %s logged:\n%s", reason, log)
	}
}

// TestL16NetworkSelector: a managed NIC's network selector names the
// project that holds the network: the instance's own with
// features.networks=true, default otherwise.
func TestL16NetworkSelector(t *testing.T) {
	for features, want := range map[string]string{"true": "network:other/lxdbr0", "false": "network:default/lxdbr0"} {
		h := newHarness(t, "")
		h.addInstance("other", "c1", "container", uuidA)
		h.fake.SetDevice("other", "c1", "eth0", map[string]string{"type": "nic", "network": "lxdbr0"})
		h.fake.AddProject("other", map[string]string{"features.networks": features})
		attrs, err := h.attest("other", newAgent("c1"))
		if err != nil {
			t.Fatalf("features.networks=%s: %v\n%s", features, err, h.log)
		}
		if !contains(attrs.GetSelectorValues(), want) {
			t.Errorf("features.networks=%s: %v, want %s", features, attrs.GetSelectorValues(), want)
		}
	}
}

// TestSEC09InflightRefused: with max_inflight = 1, an attestation while
// another runs is refused before any LXD call.
func TestSEC09InflightRefused(t *testing.T) {
	h := newHarness(t, "limits {\n  max_inflight = 1\n}")
	h.addInstance("default", "c1", "container", uuidA)
	h.addInstance("default", "c2", "container", uuidB)
	challenged, release := make(chan struct{}), make(chan struct{})
	first := newAgent("c1")
	first.answer = func(h *harness, a *agent, c *lxdiidv1.Challenge) *lxdiidv1.ChallengeResponse {
		close(challenged)
		<-release
		h.writeProof("default", a, c, 0, 0o600, a.rightProof(c))
		return done
	}
	errs := make(chan error, 1)
	go func() {
		_, err := h.attest("default", first)
		errs <- err
	}()
	<-challenged
	before := len(h.fake.Requests())
	_, err := h.attest("default", newAgent("c2"))
	checkRefusedWith(t, h, err, "rate_limited")
	if len(h.fake.Requests()) != before {
		t.Error("the refused attestation reached LXD")
	}
	close(release)
	if err := <-errs; err != nil {
		t.Errorf("the first attestation: %v", err)
	}
}

// TestSEC09CooldownAttest: a claim that failed is refused until its
// cooldown ends, then tried again.
func TestSEC09CooldownAttest(t *testing.T) {
	h := newHarness(t, "limits {\n  failure_cooldown = \"200ms\"\n}")
	_, err := h.attest("default", newAgent("nobody"))
	checkRefusedWith(t, h, err, "instance_not_found")
	_, err = h.attest("default", newAgent("nobody"))
	checkRefusedWith(t, h, err, "rate_limited")
	time.Sleep(250 * time.Millisecond)
	h.addInstance("default", "nobody", "container", uuidA)
	if _, err := h.attest("default", newAgent("nobody")); err != nil {
		t.Errorf("after the cooldown: %v\n%s", err, h.log)
	}
}

// TestFRS15Breaker: transport failures in a row open the server's breaker;
// while open, attestations fail without calling LXD; after breaker_open a
// trial goes through, and its success closes it.
func TestFRS15Breaker(t *testing.T) {
	h := newHarness(t, "limits {\n  breaker_threshold = 2\n  breaker_open = \"1s\"\n}")
	h.addInstance("default", "c4", "container", uuidA)
	h.fake.FailAll(500, "database is not available")
	for _, name := range []string{"c1", "c2"} {
		_, err := h.attest("default", newAgent(name))
		checkRefusedWith(t, h, err, "lxd_unavailable")
	}
	before := len(h.fake.Requests())
	_, err := h.attest("default", newAgent("c3"))
	checkRefusedWith(t, h, err, "lxd_unavailable")
	if len(h.fake.Requests()) != before || !strings.Contains(h.log.String(), "circuit breaker is open") {
		t.Fatalf("FR-S15: an open breaker let %d requests through:\n%s", len(h.fake.Requests())-before, h.log)
	}
	h.fake.FailAll(0, "")
	// another claim than the trial's: a failure starts its claim's cooldown
	if _, err := h.attest("default", newAgent("c5")); err == nil {
		t.Fatal("FR-S15: an attestation got through before breaker_open ended")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := h.attest("default", newAgent("c4")); err != nil {
		t.Fatalf("the trial after breaker_open: %v\n%s", err, h.log)
	}
}

// checkRefusedWith is checkRefused for a harness that ran several
// attestations: the last reason logged must be reason.
func checkRefusedWith(t *testing.T, h *harness, err error, reason string) {
	t.Helper()
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "attestation failed" {
		t.Fatalf("SEC-10: %v, want PermissionDenied: attestation failed", err)
	}
	log := h.log.String()
	if i := strings.LastIndex(log, "reason_code="); i < 0 || !strings.HasPrefix(log[i:], "reason_code="+reason) {
		t.Errorf("last reason is not %s:\n%s", reason, log)
	}
}

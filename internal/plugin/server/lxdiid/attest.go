package lxdiid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/canonical/lxd/shared/api"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	proofpkg "github.com/dihedron/lxd-spiffe/internal/proof"
	lxdiidv1 "github.com/dihedron/lxd-spiffe/internal/proto"
	"github.com/dihedron/lxd-spiffe/internal/selectors"
)

// The reason codes of the server's log (Observability).
// The instance keys that identify an instance (R1).
const (
	keyUUID       = "volatile.uuid"
	keyGeneration = "volatile.uuid.generation"
)

const (
	reasonOK                = "ok"
	reasonPayloadInvalid    = "payload_invalid"
	reasonNotFound          = "instance_not_found"
	reasonAmbiguous         = "instance_ambiguous"
	reasonPolicyDenied      = "policy_denied"
	reasonProofTimeout      = "proof_timeout"
	reasonProofMismatch     = "proof_mismatch"
	reasonVMAgent           = "vm_agent_unavailable"
	reasonLXDUnavailable    = "lxd_unavailable"
	reasonLXDForbidden      = "lxd_forbidden"
	reasonExtensionMissing  = "extension_missing"
	reasonInstanceMoved     = "instance_moved"
	reasonProjectUnreadable = "project_unreadable"
	reasonRateLimited       = "rate_limited"
)

const (
	// proofFileMode is the file_pull proof file's only accepted mode.
	proofFileMode = 0o600
	// proofDirMode is the proof directory's mode, checked as defence in
	// depth (Mode file_pull, step 6).
	proofDirMode = 0o700
	// lxdSlack bounds the LXD calls around the proof, which proof_timeout
	// bounds.
	lxdSlack = 30 * time.Second
)

// failure is a refused attestation: its reason code and the detail that
// only the server's log gets (SEC-10).
type failure struct {
	reason string
	err    error
}

func (f *failure) Error() string { return f.reason + ": " + f.err.Error() }

func fail(reason, format string, a ...any) *failure {
	return &failure{reason: reason, err: fmt.Errorf(format, a...)}
}

// denied is the failure of a policy check, with the policy's own reason;
// when, if set, tells the log when the check ran.
func denied(err error, when string) *failure {
	if when != "" {
		err = fmt.Errorf("%s: %w", when, err)
	}
	var d *policy.Denial
	if errors.As(err, &d) {
		return &failure{reason: string(d.Code), err: err}
	}
	return &failure{reason: reasonPolicyDenied, err: err}
}

// attestation is one run of the Attest stream.
type attestation struct {
	s      *settings
	stream nodeattestorv1.NodeAttestor_AttestServer
	log    *slog.Logger
	// claimKey is the claim's cooldown key (SEC-09), once it is decoded.
	claimKey string
	// proof is how long the challenge took, sent to proof read back.
	proof time.Duration
	start time.Time
	// inflight holds the config_push keys of the attestations running in
	// this plugin process.
	inflight *challenges
	// policy and hint are those of the decision, applied again to every
	// re-read before a write (FR-S14).
	policy policy.Policy
	hint   string
}

// match is the one instance the claim resolved to (FR-S3).
type match struct {
	srv      *lxdServer
	client   *lxd.Client
	inst     *api.Instance
	etag     string // inst's, the If-Match of the challenge write (FR-S14)
	instance string // inst.Name, as asked for
}

func newCorrelationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return hex.EncodeToString(b)
}

// run attests the agent on the stream: payload, resolution, policy,
// challenge, proof, re-verification, identity. A *failure tells why it
// refused.
func (a *attestation) run(ctx context.Context) (*nodeattestorv1.AgentAttributes, error) {
	ctx, cancel := context.WithTimeout(ctx, a.s.proofTimeout+lxdSlack)
	defer cancel()
	req, err := a.stream.Recv()
	if err != nil {
		return nil, fail(reasonPayloadInvalid, "receiving the payload: %v", err)
	}
	r, err := lxdiidv1.DecodeAttestRequest(req.GetPayload())
	if err != nil {
		return nil, fail(reasonPayloadInvalid, "%v", err)
	}
	a.claimKey = r.GetClaim().GetProject() + "/" + r.GetClaim().GetInstanceName()
	if a.s.limiter.cooling(a.claimKey) {
		return nil, fail(reasonRateLimited, "the claim is cooling down after a failure")
	}
	mode, err := a.chooseMode(r.GetProofModes())
	if err != nil {
		return nil, err
	}
	a.log = a.log.With("proof_mode", string(mode))
	m, err := a.resolve(ctx, r.GetClaim())
	if err != nil {
		return nil, err
	}
	a.log = a.log.With("server_id", m.srv.id, "project", m.inst.Project)
	a.policy = a.s.policy
	a.policy.Projects = m.srv.projects
	a.hint = typeHint(r.GetInstanceType())
	decision, err := policy.Check(m.inst, a.policy, mode, a.hint)
	if err != nil {
		return nil, denied(err, "")
	}
	uuid := m.inst.ExpandedConfig[keyUUID]
	if err := a.unique(ctx, m, uuid); err != nil {
		return nil, err
	}
	switch mode {
	case policy.ClaimOnly:
		a.log.Warn(`proof_mode "none": issuing an identity on a claim only, with no proof of co-location (SEC-02)`)
	case policy.ConfigPush:
		err = a.configPush(ctx, m, r)
	default:
		err = a.filePull(ctx, m, r, decision.ExpectedOwner)
	}
	if err != nil {
		return nil, err
	}
	if mode != policy.ClaimOnly {
		if err := a.reverify(ctx, m); err != nil {
			return nil, err
		}
	}
	return a.identity(ctx, m, uuid)
}

// chooseMode applies Mode negotiation: the configured mode, which the agent
// must offer; auto takes file_pull when offered, then config_push.
func (a *attestation) chooseMode(offered []lxdiidv1.ProofMode) (policy.Mode, error) {
	filePull := slices.Contains(offered, lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL)
	configPush := slices.Contains(offered, lxdiidv1.ProofMode_PROOF_MODE_CONFIG_PUSH)
	switch {
	case a.s.mode == modeNone:
		return policy.ClaimOnly, nil
	case filePull && (a.s.mode == modeFilePull || a.s.mode == modeAuto):
		return policy.FilePull, nil
	case configPush && (a.s.mode == modeConfigPush || a.s.mode == modeAuto):
		return policy.ConfigPush, nil
	}
	return "", fail(reasonPolicyDenied, "the agent offers %v, the server requires %s", offered, a.s.mode)
}

func typeHint(t lxdiidv1.InstanceType) string {
	switch t {
	case lxdiidv1.InstanceType_INSTANCE_TYPE_CONTAINER:
		return string(api.InstanceTypeContainer)
	case lxdiidv1.InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE:
		return string(api.InstanceTypeVM)
	}
	return ""
}

// lxdReason is the reason of a failed LXD call: lxd_forbidden when LXD
// refused SPIRE's identity (401, 403), lxd_unavailable otherwise.
func lxdReason(err error) string {
	if lxd.KindOf(err) == lxd.KindAuth {
		return reasonLXDForbidden
	}
	return reasonLXDUnavailable
}

// client returns a server's checked client, or the failure of its check.
func client(ctx context.Context, srv *lxdServer) (*lxd.Client, error) {
	c, err := srv.checked(ctx)
	switch {
	case errors.Is(err, lxd.ErrMissingExtensions):
		return nil, fail(reasonExtensionMissing, "%v", err)
	case err != nil:
		return nil, fail(lxdReason(err), "%v", err)
	}
	return c, nil
}

// resolve finds the one instance the claim names, across every server and
// project of the allow-lists, or only the claimed project (FR-S3, SEC-05).
// A server that cannot be asked fails the attestation: without it, the
// match could not be shown to be the only one.
func (a *attestation) resolve(ctx context.Context, c *lxdiidv1.Claim) (*match, error) {
	var matches []*match
	scoped := false
	for _, srv := range a.s.servers {
		projects := srv.projects
		if c.GetProject() != "" {
			if !slices.Contains(projects, c.GetProject()) {
				continue
			}
			projects = []string{c.GetProject()}
		}
		scoped = true
		cl, err := client(ctx, srv)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			inst, etag, err := cl.Instance(ctx, p, c.GetInstanceName())
			srv.observe(ctx, err)
			switch {
			case lxd.KindOf(err) == lxd.KindNotFound:
				continue
			case err != nil:
				return nil, fail(lxdReason(err), "server %s, project %s: %v", srv.id, p, err)
			case c.GetCloudInitInstanceId() != "" && inst.ExpandedConfig["volatile.cloud-init.instance-id"] != c.GetCloudInitInstanceId():
				continue
			}
			matches = append(matches, &match{srv: srv, client: cl, inst: inst, etag: etag, instance: c.GetInstanceName()})
		}
	}
	switch {
	case !scoped:
		return nil, fail("scope_denied", "the claimed project is in no server's allow-list")
	case len(matches) == 0:
		return nil, fail(reasonNotFound, "no instance matches the claim")
	case len(matches) > 1:
		return nil, fail(reasonAmbiguous, "%d instances match the claim", len(matches))
	}
	return matches[0], nil
}

// unique requires the match to be the only instance with its volatile.uuid
// on every server and project of the allow-lists (FR-S18).
func (a *attestation) unique(ctx context.Context, m *match, uuid string) error {
	var found []string
	for _, srv := range a.s.servers {
		cl, err := client(ctx, srv)
		if err != nil {
			return err
		}
		for _, p := range srv.projects {
			names, err := cl.InstancesByUUID(ctx, p, uuid)
			srv.observe(ctx, err)
			if err != nil {
				return fail(lxdReason(err), "server %s, project %s: listing by uuid: %v", srv.id, p, err)
			}
			for _, n := range names {
				found = append(found, srv.id+"/"+p+"/"+n)
			}
		}
	}
	if len(found) != 1 || found[0] != m.srv.id+"/"+m.inst.Project+"/"+m.instance {
		return fail(reasonAmbiguous, "instances with the resolved uuid: %v", found)
	}
	return nil
}

// filePull runs the file_pull challenge (Mode file_pull): a fresh server
// nonce, valid for proof_timeout only (SEC-03), the proof path derived from
// the agent nonce (SEC-04), and the proof file read back.
func (a *attestation) filePull(ctx context.Context, m *match, r *lxdiidv1.AttestRequest, owner uint32) error {
	serverNonce, err := proofpkg.NewNonce()
	if err != nil {
		return fail(reasonPolicyDenied, "%v", err)
	}
	path, err := proofpkg.Path(a.s.proofDir, r.GetAgentNonce())
	if err != nil {
		return fail(reasonPayloadInvalid, "%v", err)
	}
	sum, err := proofpkg.Compute(r.GetClaim().GetProject(), r.GetClaim().GetInstanceName(), serverNonce, r.GetAgentNonce())
	if err != nil {
		return fail(reasonPayloadInvalid, "%v", err)
	}
	start := time.Now()
	defer func() { a.proof = time.Since(start) }()
	deadline := start.Add(a.s.proofTimeout)
	challenge := &lxdiidv1.Challenge{
		Version:     lxdiidv1.Version,
		Mode:        lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL,
		ServerNonce: serverNonce,
		ProofPath:   path,
		TimeoutMs:   uint32(a.s.proofTimeout.Milliseconds()), //nolint:gosec // proof_timeout is at most 2 minutes
	}
	data, err := proto.Marshal(challenge)
	if err != nil {
		return fail(reasonPolicyDenied, "%v", err)
	}
	if err := a.stream.Send(&nodeattestorv1.AttestResponse{Response: &nodeattestorv1.AttestResponse_Challenge{Challenge: data}}); err != nil {
		return fail(reasonProofTimeout, "sending the challenge: %v", err)
	}
	resp, err := a.receive(deadline)
	if err != nil {
		return err
	}
	if resp.GetMode() != challenge.GetMode() || !resp.GetDone() {
		return fail(reasonProofMismatch, "the agent did not complete the file_pull step")
	}
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	isVM := m.inst.Type == string(api.InstanceTypeVM)
	file, err := readProof(pctx, m, path, isVM)
	if err != nil {
		return err
	}
	switch {
	case file.Type != "file":
		return fail(reasonProofMismatch, "the proof is a %s, not a regular file", file.Type)
	case file.UID != int64(owner) || file.Mode != proofFileMode:
		return fail(reasonProofMismatch, "the proof has owner %d and mode %04o, not %d and %04o", file.UID, file.Mode, owner, proofFileMode)
	case len(file.Content) != limits.HexProofSize || !proofpkg.Equal(file.Content, proofpkg.Hex(sum)):
		return fail(reasonProofMismatch, "the proof's content is not the expected proof")
	}
	dir, err := m.client.File(pctx, m.inst.Project, m.instance, a.s.proofDir)
	m.srv.observe(pctx, err)
	switch {
	case err != nil:
		return fail(reasonProofMismatch, "reading the proof directory: %v", err)
	case dir.Type != "directory" || dir.UID != int64(owner) || dir.Mode != proofDirMode:
		return fail(reasonProofMismatch, "the proof directory is a %s with owner %d and mode %04o", dir.Type, dir.UID, dir.Mode)
	}
	return nil
}

// receive waits for the agent's ChallengeResponse until deadline.
func (a *attestation) receive(deadline time.Time) (*lxdiidv1.ChallengeResponse, error) {
	type result struct {
		req *nodeattestorv1.AttestRequest
		err error
	}
	ch := make(chan result, 1)
	go func() {
		// returns when the stream ends, at the latest when Attest returns
		req, err := a.stream.Recv()
		ch <- result{req, err}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil, fail(reasonProofTimeout, "no answer to the challenge within proof_timeout")
	case res := <-ch:
		if res.err != nil {
			return nil, fail(reasonProofTimeout, "receiving the answer: %v", res.err)
		}
		resp, err := lxdiidv1.DecodeChallengeResponse(res.req.GetChallengeResponse())
		if err != nil {
			return nil, fail(reasonPayloadInvalid, "%v", err)
		}
		return resp, nil
	}
}

// readProof reads the proof file, retrying while a VM's lxd-agent starts:
// its 500 and, for a VM, a 404 just after (gate L7), with a backoff of
// 250 ms doubling up to 2 s, until ctx's deadline. Any other error fails
// at once.
func readProof(ctx context.Context, m *match, path string, isVM bool) (*lxd.File, error) {
	wait := 250 * time.Millisecond
	reason := reasonProofTimeout
	for {
		file, err := m.client.File(ctx, m.inst.Project, m.instance, path)
		m.srv.observe(ctx, err)
		kind := lxd.KindOf(err)
		switch {
		case err == nil:
			return file, nil
		case kind == lxd.KindAgentNotRunning:
			reason = reasonVMAgent
		case kind == lxd.KindNotFound && isVM:
			reason = reasonProofTimeout
		case kind == lxd.KindNotFound:
			return nil, fail(reasonProofMismatch, "no proof file: %v", err)
		case kind == lxd.KindTimeout || ctx.Err() != nil:
			return nil, fail(reason, "the proof file was not readable within proof_timeout: %v", err)
		default:
			return nil, fail(lxdReason(err), "reading the proof file: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil, fail(reason, "the proof file was not readable within proof_timeout: %v", err)
		case <-time.After(wait):
		}
		wait = min(2*wait, 2*time.Second)
	}
}

// reverify reads the instance again after the proof: it must still run,
// with the same identity, generation, project, name and location (FR-S10,
// SEC-06).
func (a *attestation) reverify(ctx context.Context, m *match) error {
	again, _, err := m.client.Instance(ctx, m.inst.Project, m.instance)
	m.srv.observe(ctx, err)
	switch {
	case lxd.KindOf(err) == lxd.KindNotFound:
		return fail(reasonNotFound, "the instance is gone after the proof")
	case err != nil:
		return fail(lxdReason(err), "reading the instance again: %v", err)
	case again.Status != "Running":
		return fail("not_running", "the instance is %s after the proof", again.Status)
	}
	return unchanged(m.inst, again, "during the proof")
}

// unchanged requires again to have first's identity, generation, project,
// name and location; when tells the log when it changed.
func unchanged(first, again *api.Instance, when string) error {
	before, after := first.ExpandedConfig, again.ExpandedConfig
	switch {
	case again.Name != first.Name || again.Project != first.Project ||
		after[keyUUID] != before[keyUUID] || after[keyGeneration] != before[keyGeneration]:
		return fail(reasonNotFound, "the instance's identity changed %s", when)
	case again.Location != first.Location:
		return fail(reasonInstanceMoved, "the instance moved from %q to %q %s", first.Location, again.Location, when)
	}
	return nil
}

// identity builds the agent's SPIFFE ID and selectors (FR-S7, FR-S8,
// FR-S9), from the decision read only (FR-S4).
func (a *attestation) identity(ctx context.Context, m *match, uuid string) (*nodeattestorv1.AgentAttributes, error) {
	in := selectors.Input{ServerID: m.srv.id, Instance: m.inst}
	if selectors.NeedsNetworkProject(a.s.selectors, m.inst) {
		p, _, err := m.client.Project(ctx, m.inst.Project)
		m.srv.observe(ctx, err)
		if err != nil {
			return nil, fail(reasonProjectUnreadable, "reading project %s: %v", m.inst.Project, err)
		}
		in.NetworkProject = "default"
		if p.Config["features.networks"] == "true" {
			in.NetworkProject = m.inst.Project
		}
	}
	values, omitted, err := selectors.Build(a.s.selectors, in)
	if err != nil {
		return nil, fail(reasonPolicyDenied, "selectors: %v", err)
	}
	for _, o := range omitted {
		a.log.Warn("selector omitted", "selector", o.Selector, "why", o.Reason)
	}
	path, err := a.s.agentPath(m.srv.id, uuid)
	if err != nil {
		return nil, fail(reasonPolicyDenied, "%v", err)
	}
	a.log = a.log.With("instance_uuid", uuid)
	a.log.Info("attestation succeeded", "outcome", "success", "reason_code", reasonOK, "selectors", values,
		"duration_ms", time.Since(a.start).Milliseconds(), "proof_ms", a.proof.Milliseconds())
	return &nodeattestorv1.AgentAttributes{
		SpiffeId:       "spiffe://" + a.s.trustDomain + "/spire/agent/" + path,
		SelectorValues: values,
		CanReattest:    a.s.reattest,
	}, nil
}

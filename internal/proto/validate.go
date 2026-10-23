package lxdiidv1

import (
	"errors"
	"fmt"
	"path"

	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/proof"
)

// Version is the only protocol version.
const Version = 1

// ErrInvalid is wrapped by every refusal of a message; the detail is for
// the server log only (SEC-10).
var ErrInvalid = errors.New("invalid message")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// decode bounds the size (SEC-11), unmarshals and refuses unknown fields:
// a message of version 1 has exactly the fields of the spec.
func decode(data []byte, m proto.Message) error {
	if len(data) > limits.MaxPayload {
		return invalid("message of %d bytes", len(data))
	}
	if err := proto.Unmarshal(data, m); err != nil {
		return invalid("%v", err)
	}
	if len(m.ProtoReflect().GetUnknown()) > 0 {
		return invalid("unknown fields")
	}
	return nil
}

// DecodeAttestRequest decodes and validates the agent's payload before any
// LXD call (FR-S2).
func DecodeAttestRequest(data []byte) (*AttestRequest, error) {
	r := &AttestRequest{}
	if err := decode(data, r); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// Validate checks an AttestRequest (FR-S2): version, claim syntax, nonce
// length, instance type, proof modes and string bounds.
func (r *AttestRequest) Validate() error {
	switch {
	case r.GetVersion() != Version:
		return invalid("version %d", r.GetVersion())
	case r.GetClaim() == nil:
		return invalid("no claim")
	case len(r.GetClaim().ProtoReflect().GetUnknown()) > 0:
		return invalid("unknown fields in the claim")
	case len(r.GetAgentNonce()) != limits.NonceSize:
		return invalid("agent nonce of %d bytes", len(r.GetAgentNonce()))
	case InstanceType_name[int32(r.GetInstanceType())] == "":
		return invalid("instance type %d", r.GetInstanceType())
	case len(r.GetPluginVersion()) > limits.MaxString:
		return invalid("plugin version of %d bytes", len(r.GetPluginVersion()))
	case len(r.GetProofModes()) > limits.MaxProofModes:
		return invalid("%d proof modes", len(r.GetProofModes()))
	}
	c := r.GetClaim()
	if ClaimSource_name[int32(c.GetNameSource())] == "" {
		return invalid("claim source %d", c.GetNameSource())
	}
	if err := (claim.Claim{Project: c.GetProject(), InstanceName: c.GetInstanceName(), CloudInitInstanceID: c.GetCloudInitInstanceId()}).Validate(); err != nil {
		return invalid("%v", err)
	}
	seen := map[ProofMode]bool{}
	for _, m := range r.GetProofModes() {
		if !isProofMode(m) || seen[m] {
			return invalid("proof modes %v", r.GetProofModes())
		}
		seen[m] = true
	}
	return nil
}

// DecodeChallenge decodes and validates the server's challenge, on the
// agent (FR-A6). The agent still checks the proof path against its own
// proof_dir and derivation (FR-A7).
func DecodeChallenge(data []byte) (*Challenge, error) {
	c := &Challenge{}
	if err := decode(data, c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks a Challenge: version, a known mode, the fields that mode
// uses and no others, and a timeout.
func (c *Challenge) Validate() error {
	switch {
	case c.GetVersion() != Version:
		return invalid("version %d", c.GetVersion())
	case c.GetTimeoutMs() == 0:
		return invalid("no timeout")
	}
	switch c.GetMode() {
	case ProofMode_PROOF_MODE_FILE_PULL:
		p := c.GetProofPath()
		switch {
		case len(c.GetServerNonce()) != limits.NonceSize:
			return invalid("server nonce of %d bytes", len(c.GetServerNonce()))
		case len(p) > limits.MaxString || !path.IsAbs(p) || path.Clean(p) != p:
			return invalid("proof path of %d bytes, not absolute or not clean", len(p))
		case c.GetProofKey() != "":
			return invalid("file_pull challenge with a proof key")
		}
	case ProofMode_PROOF_MODE_CONFIG_PUSH:
		switch {
		case len(c.GetServerNonce()) != 0:
			return invalid("config_push challenge with a server nonce")
		case c.GetProofPath() != "":
			return invalid("config_push challenge with a proof path")
		case !proof.IsKey(c.GetProofKey()):
			return invalid("proof key is not a challenge key")
		}
	default:
		return invalid("proof mode %d", c.GetMode())
	}
	return nil
}

// DecodeChallengeResponse decodes and validates the agent's response, on
// the server. The caller checks that its mode equals the challenge's.
func DecodeChallengeResponse(data []byte) (*ChallengeResponse, error) {
	r := &ChallengeResponse{}
	if err := decode(data, r); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// Validate checks a ChallengeResponse: version, a known mode, and an echo
// of limits.HexProofSize lowercase hex characters exactly when a
// config_push step is done; done=false reports a failed guest step.
func (r *ChallengeResponse) Validate() error {
	if r.GetVersion() != Version {
		return invalid("version %d", r.GetVersion())
	}
	switch r.GetMode() {
	case ProofMode_PROOF_MODE_FILE_PULL:
		if len(r.GetEcho()) != 0 {
			return invalid("file_pull response with an echo")
		}
	case ProofMode_PROOF_MODE_CONFIG_PUSH:
		if r.GetDone() && !proof.IsHexProof(r.GetEcho()) {
			return invalid("config_push echo of %d bytes or not lowercase hex", len(r.GetEcho()))
		}
		if !r.GetDone() && len(r.GetEcho()) != 0 {
			return invalid("config_push echo without done")
		}
	default:
		return invalid("proof mode %d", r.GetMode())
	}
	return nil
}

func isProofMode(m ProofMode) bool {
	return m == ProofMode_PROOF_MODE_FILE_PULL || m == ProofMode_PROOF_MODE_CONFIG_PUSH
}

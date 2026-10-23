package lxdiid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"slices"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/agentproof"
	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/proof"
	lxdiidv1 "github.com/dihedron/lxd-spiffe/internal/proto"
	"github.com/dihedron/lxd-spiffe/pkg/metadata"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// received is one result of the stream's Recv.
type received struct {
	challenge *nodeattestorv1.Challenge
	err       error
}

// attest sends the claim (FR-A2–A5), answers the challenge (FR-A6, FR-A7)
// and keeps the proof until the stream ends or proof_timeout passes. Logs
// carry the claim and the steps, never a nonce or a proof (FR-A10).
func (s *settings) attest(ctx context.Context, stream nodeattestorv1.NodeAttestor_AidAttestationServer) error {
	src := claim.Sources{Hostname: os.Hostname}
	g, typ, modes, err := s.probeDevLXD(ctx)
	if err != nil {
		return err
	}
	if g != nil {
		src.DevLXD = g
	}
	c, err := claim.Collect(ctx, s.claim, src)
	if err != nil {
		return err
	}
	source := lxdiidv1.ClaimSource(c.NameSource) //nolint:gosec // G115: one of claim's four sources
	nonce, err := proof.NewNonce()
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(&lxdiidv1.AttestRequest{
		Version: lxdiidv1.Version,
		Claim: &lxdiidv1.Claim{
			Project: c.Project, InstanceName: c.InstanceName, CloudInitInstanceId: c.CloudInitInstanceID,
			NameSource: source,
		},
		AgentNonce:    nonce,
		InstanceType:  typ,
		ProofModes:    modes,
		PluginVersion: metadata.Version,
	})
	if err != nil {
		return err
	}
	slog.Info("attesting", "project", c.Project, "instance_name", c.InstanceName,
		"name_source", source.String(), "instance_type", typ.String())
	if err := stream.Send(&nodeattestorv1.PayloadOrChallengeResponse{Data: &nodeattestorv1.PayloadOrChallengeResponse_Payload{Payload: payload}}); err != nil {
		return fmt.Errorf("sending the claim: %w", err)
	}
	messages := receive(stream)
	m, err := next(ctx, messages)
	if ended(err) {
		return nil // the server asked for no proof (proof_mode none)
	}
	if err != nil {
		return fmt.Errorf("waiting for the challenge: %w", err)
	}
	remove, answer, err := s.prove(ctx, g, modes, c, nonce, m.GetChallenge())
	if err != nil {
		return err
	}
	defer remove()
	mode := answer.GetMode()
	response, err := proto.Marshal(answer)
	if err != nil {
		return err
	}
	if err := stream.Send(&nodeattestorv1.PayloadOrChallengeResponse{Data: &nodeattestorv1.PayloadOrChallengeResponse_ChallengeResponse{ChallengeResponse: response}}); err != nil {
		return fmt.Errorf("sending the challenge response: %w", err)
	}
	slog.Debug("challenge answered, waiting for the server", "proof_mode", mode.String())
	pctx, cancel := context.WithTimeoutCause(ctx, s.proofTimeout, fmt.Errorf("proof_timeout of %v: %w", s.proofTimeout, context.DeadlineExceeded))
	defer cancel()
	switch _, err := next(pctx, messages); {
	case ended(err):
		return nil
	case err == nil:
		return errors.New("the server sent a second challenge")
	default:
		return fmt.Errorf("waiting for the server to read the proof: %w", err)
	}
}

// probeDevLXD connects to devLXD and reads the instance type (FR-A3); the
// guest is nil without a socket, and config_push is offered only when
// devLXD answers (FR-A5).
func (s *settings) probeDevLXD(ctx context.Context) (*guest, lxdiidv1.InstanceType, []lxdiidv1.ProofMode, error) {
	modes := []lxdiidv1.ProofMode{lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL}
	g, err := s.connectDevLXD(ctx)
	if err != nil || g == nil {
		return nil, lxdiidv1.InstanceType_INSTANCE_TYPE_UNSPECIFIED, modes, err
	}
	typ, err := g.instanceType(ctx)
	switch {
	case err != nil && s.devLXD == devLXDRequired:
		return nil, 0, nil, fmt.Errorf("reading devLXD: %w", err)
	case err != nil:
		slog.Debug("devLXD gave no instance type", "error", err.Error())
		return g, lxdiidv1.InstanceType_INSTANCE_TYPE_UNSPECIFIED, modes, nil
	}
	return g, typ, append(modes, lxdiidv1.ProofMode_PROOF_MODE_CONFIG_PUSH), nil
}

// prove checks the challenge and answers it: file_pull writes the proof
// file (FR-A7), config_push reads the challenge key through devLXD
// (FR-A8). remove deletes what the answer left in the guest.
func (s *settings) prove(ctx context.Context, g *guest, offered []lxdiidv1.ProofMode, c claim.Claim, nonce, data []byte) (remove func(), answer *lxdiidv1.ChallengeResponse, err error) {
	ch, err := lxdiidv1.DecodeChallenge(data)
	if err != nil {
		return nil, nil, fmt.Errorf("the challenge: %w", err)
	}
	answer = &lxdiidv1.ChallengeResponse{Version: lxdiidv1.Version, Mode: ch.GetMode(), Done: true}
	switch {
	case !slices.Contains(offered, ch.GetMode()):
		err = fmt.Errorf("the server asks for %v, which this agent does not offer", ch.GetMode())
	case ch.GetMode() == lxdiidv1.ProofMode_PROOF_MODE_FILE_PULL:
		remove, err = s.filePull(c, nonce, ch)
	case ch.GetMode() == lxdiidv1.ProofMode_PROOF_MODE_CONFIG_PUSH:
		remove = func() {
			// nothing to remove: the server removes its challenge key
		}
		answer.Echo, err = configPush(ctx, g, nonce, ch)
	default:
		err = fmt.Errorf("unknown proof mode %v", ch.GetMode())
	}
	if err != nil {
		return nil, nil, err
	}
	return remove, answer, nil
}

// filePull writes the proof file of the challenge (FR-A7).
func (s *settings) filePull(c claim.Claim, nonce []byte, ch *lxdiidv1.Challenge) (remove func(), err error) {
	want, err := proof.Path(s.proofDir, nonce)
	if err != nil {
		return nil, err
	}
	if ch.GetProofPath() != want {
		return nil, fmt.Errorf("the challenge's proof path %q is not %q: check that proof_dir is the same on the server", ch.GetProofPath(), want)
	}
	sum, err := proof.Compute(c.Project, c.InstanceName, ch.GetServerNonce(), nonce)
	if err != nil {
		return nil, err
	}
	if remove, err = agentproof.WriteFile(s.proofDir, path.Base(want), proof.Hex(sum), requiredUID); err != nil {
		return nil, fmt.Errorf("writing the proof: %w", err)
	}
	return remove, nil
}

// configPush reads the challenge key, which must be the one derived from
// this agent's nonce (SEC-04), and returns its value, 64 hex characters
// (FR-A8).
func configPush(ctx context.Context, g *guest, nonce []byte, ch *lxdiidv1.Challenge) ([]byte, error) {
	want, err := proof.Key(nonce)
	if err != nil {
		return nil, err
	}
	if ch.GetProofKey() != want {
		return nil, fmt.Errorf("the challenge's key %q is not %q, the one of this agent's nonce", ch.GetProofKey(), want)
	}
	value, err := g.config(ctx, want)
	if err != nil {
		return nil, fmt.Errorf("reading the challenge key through devLXD: %w", err)
	}
	if !proof.IsHexProof([]byte(value)) {
		return nil, fmt.Errorf("the challenge key holds %d bytes, not 64 lowercase hex characters", len(value))
	}
	return []byte(value), nil
}

// receive reads the stream until it fails or the stream's context ends, so
// that a wait for a message can also end on a deadline.
func receive(stream nodeattestorv1.NodeAttestor_AidAttestationServer) <-chan received {
	messages := make(chan received)
	go func() {
		for {
			m, err := stream.Recv()
			select {
			case messages <- received{challenge: m, err: err}:
			case <-stream.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return messages
}

// next waits for the next message, until ctx ends with its cause.
func next(ctx context.Context, messages <-chan received) (*nodeattestorv1.Challenge, error) {
	select {
	case r := <-messages:
		return r.challenge, r.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// ended reports a stream closed by the other side: what the SPIRE Agent
// does once the server has answered.
func ended(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

// timeoutCause is the cause of attestation_timeout's expiry.
func timeoutCause(d time.Duration) error {
	return fmt.Errorf("attestation_timeout of %v: %w", d, context.DeadlineExceeded)
}

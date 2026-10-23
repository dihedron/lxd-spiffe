package lxdinstance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/agentproof"
	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/proof"
	lxdinstancev1 "github.com/dihedron/lxd-spiffe/internal/proto"
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
	typ := lxdinstancev1.InstanceType_INSTANCE_TYPE_UNSPECIFIED
	g, err := s.connectDevLXD(ctx)
	if err != nil {
		return err
	}
	if g != nil {
		src.DevLXD = g
		if typ, err = g.instanceType(ctx); err != nil {
			if s.devLXD == devLXDRequired {
				return fmt.Errorf("reading devLXD: %w", err)
			}
			slog.Debug("devLXD gave no instance type", "error", err.Error())
		}
	}
	c, err := claim.Collect(ctx, s.claim, src)
	if err != nil {
		return err
	}
	source := lxdinstancev1.ClaimSource(c.NameSource) //nolint:gosec // G115: one of claim's four sources
	nonce, err := proof.NewNonce()
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(&lxdinstancev1.AttestRequest{
		Version: lxdinstancev1.Version,
		Claim: &lxdinstancev1.Claim{
			Project: c.Project, InstanceName: c.InstanceName, CloudInitInstanceId: c.CloudInitInstanceID,
			NameSource: source,
		},
		AgentNonce:    nonce,
		InstanceType:  typ,
		ProofModes:    []lxdinstancev1.ProofMode{lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL},
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
	remove, mode, err := s.prove(c, nonce, m.GetChallenge())
	if err != nil {
		return err
	}
	defer remove()
	response, err := proto.Marshal(&lxdinstancev1.ChallengeResponse{Version: lxdinstancev1.Version, Mode: mode, Done: true})
	if err != nil {
		return err
	}
	if err := stream.Send(&nodeattestorv1.PayloadOrChallengeResponse{Data: &nodeattestorv1.PayloadOrChallengeResponse_ChallengeResponse{ChallengeResponse: response}}); err != nil {
		return fmt.Errorf("sending the challenge response: %w", err)
	}
	slog.Debug("proof written, waiting for the server", "proof_mode", mode.String())
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

// prove checks the challenge and writes its proof file (FR-A7).
func (s *settings) prove(c claim.Claim, nonce, data []byte) (remove func(), mode lxdinstancev1.ProofMode, err error) {
	ch, err := lxdinstancev1.DecodeChallenge(data)
	if err != nil {
		return nil, 0, fmt.Errorf("the challenge: %w", err)
	}
	if ch.GetMode() != lxdinstancev1.ProofMode_PROOF_MODE_FILE_PULL {
		return nil, 0, fmt.Errorf("the server asks for %v, which this agent does not offer", ch.GetMode())
	}
	want, err := proof.Path(s.proofDir, nonce)
	if err != nil {
		return nil, 0, err
	}
	if ch.GetProofPath() != want {
		return nil, 0, fmt.Errorf("the challenge's proof path %q is not %q: check that proof_dir is the same on the server", ch.GetProofPath(), want)
	}
	sum, err := proof.Compute(c.Project, c.InstanceName, ch.GetServerNonce(), nonce)
	if err != nil {
		return nil, 0, err
	}
	if remove, err = agentproof.WriteFile(s.proofDir, path.Base(want), proof.Hex(sum), requiredUID); err != nil {
		return nil, 0, fmt.Errorf("writing the proof: %w", err)
	}
	return remove, ch.GetMode(), nil
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

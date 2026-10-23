// Package lxdiid implements the server side of the lxd_iid SPIRE
// node attestor. It will verify the evidence an LXD instance's SPIRE Agent
// presents, then return the agent's SPIFFE ID and selectors.
//
// It attests with file_pull or config_push, or on the claim alone when
// proof_mode is none.
package lxdiid

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	"github.com/dihedron/lxd-spiffe/internal/plugin/logging"
	"github.com/hashicorp/go-hclog"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Plugin is the server-side lxd_iid node attestor.
type Plugin struct {
	nodeattestorv1.UnimplementedNodeAttestorServer
	configv1.UnimplementedConfigServer

	mu       sync.RWMutex
	settings *settings
	// inflight outlives Configure, like the attestations it tracks.
	inflight *challenges
}

// New returns an unconfigured Plugin.
func New() *Plugin {
	return &Plugin{inflight: newChallenges()}
}

// SetLogger routes the plugin's logs, and those of log/slog, to SPIRE.
func (p *Plugin) SetLogger(logger hclog.Logger) {
	slog.SetDefault(slog.New(logging.NewHandler(logger)))
}

// Configure validates the plugin_data block and replaces the settings as a
// whole; a refused configuration leaves the previous one in place.
func (p *Plugin) Configure(ctx context.Context, req *configv1.ConfigureRequest) (*configv1.ConfigureResponse, error) {
	var c Config
	if err := config.Decode(req.GetHclConfiguration(), &c, configKeys); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	s, err := newSettings(ctx, &c, req.GetCoreConfiguration().GetTrustDomain())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	p.mu.Lock()
	p.settings = s
	p.mu.Unlock()
	return &configv1.ConfigureResponse{}, nil
}

// current returns the settings in force, nil before Configure.
func (p *Plugin) current() *settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settings
}

// Attest runs one attestation (FR-S1). Every failure answers the agent with
// the same PermissionDenied (SEC-10); the server's log has the reason code
// and the detail, under a correlation ID.
func (p *Plugin) Attest(stream nodeattestorv1.NodeAttestor_AttestServer) error {
	s := p.current()
	if s == nil {
		return status.Error(codes.FailedPrecondition, "not configured")
	}
	start := time.Now()
	log := slog.Default().With("correlation_id", newCorrelationID())
	release, ok := s.limiter.acquire()
	if !ok {
		// before any LXD call (SEC-09)
		log.Error("attestation refused", "outcome", "failure", "reason_code", reasonRateLimited, "inflight", s.limiter.inflight())
		return status.Error(codes.PermissionDenied, "attestation failed")
	}
	defer release()
	a := &attestation{s: s, stream: stream, inflight: p.inflight, log: log.With("inflight", s.limiter.inflight()), start: start}
	attrs, err := a.run(stream.Context())
	reason := reasonOK
	if err != nil {
		reason = reasonPolicyDenied
		var f *failure
		if errors.As(err, &f) {
			reason = f.reason
		}
		a.log.Error("attestation refused", "outcome", "failure", "reason_code", reason, "error", err.Error(),
			"duration_ms", time.Since(start).Milliseconds(), "proof_ms", a.proof.Milliseconds())
	}
	if a.claimKey != "" && reason != reasonRateLimited {
		s.limiter.outcome(a.claimKey, err == nil)
	}
	if err != nil {
		return status.Error(codes.PermissionDenied, "attestation failed")
	}
	return stream.Send(&nodeattestorv1.AttestResponse{Response: &nodeattestorv1.AttestResponse_AgentAttributes{AgentAttributes: attrs}})
}

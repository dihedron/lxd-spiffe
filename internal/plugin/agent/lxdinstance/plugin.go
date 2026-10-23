// Package lxdinstance implements the agent side of the lxd_instance SPIRE
// node attestor: it sends the instance's claim and a nonce, then proves it
// runs inside that instance by the mode the server asks for.
//
// It proves with file_pull, or config_push when devLXD answers.
package lxdinstance

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	"github.com/dihedron/lxd-spiffe/internal/plugin/logging"
	"github.com/hashicorp/go-hclog"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Plugin is the agent-side lxd_instance node attestor.
type Plugin struct {
	nodeattestorv1.UnimplementedNodeAttestorServer
	configv1.UnimplementedConfigServer

	mu       sync.RWMutex
	settings *settings
}

// New returns an unconfigured Plugin.
func New() *Plugin {
	return &Plugin{}
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
	s, err := newSettings(&c)
	if err != nil {
		code := codes.FailedPrecondition
		if errors.Is(err, config.ErrInvalid) {
			code = codes.InvalidArgument
		}
		return nil, status.Error(code, err.Error())
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

// AidAttestation runs one attestation (FR-A1) within attestation_timeout
// (FR-A11).
func (p *Plugin) AidAttestation(stream nodeattestorv1.NodeAttestor_AidAttestationServer) error {
	s := p.current()
	if s == nil {
		return status.Error(codes.FailedPrecondition, "not configured")
	}
	ctx, cancel := context.WithTimeoutCause(stream.Context(), s.attestationTimeout, timeoutCause(s.attestationTimeout))
	defer cancel()
	if err := s.attest(ctx, stream); err != nil {
		slog.Error("attestation failed", "error", err.Error())
		code := codes.Internal
		if errors.Is(err, context.DeadlineExceeded) {
			code = codes.DeadlineExceeded
		}
		return status.Error(code, "lxd_instance: "+err.Error())
	}
	return nil
}

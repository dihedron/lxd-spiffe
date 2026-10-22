// Package lxdinstance implements the server side of the lxd_instance SPIRE
// node attestor. It will verify the evidence an LXD instance's SPIRE Agent
// presents, then return the agent's SPIFFE ID and selectors.
//
// Configure is implemented (attestor spec, Configuration → Server);
// attestation answers codes.Unimplemented until it is.
package lxdinstance

import (
	"context"
	"log/slog"
	"sync"

	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	"github.com/dihedron/lxd-spiffe/internal/plugin/logging"
	"github.com/hashicorp/go-hclog"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/server/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Plugin is the server-side lxd_instance node attestor.
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

// Attest is not implemented yet.
func (p *Plugin) Attest(stream nodeattestorv1.NodeAttestor_AttestServer) error {
	if p.current() == nil {
		return status.Error(codes.FailedPrecondition, "not configured")
	}
	return status.Error(codes.Unimplemented, "lxd_instance: attestation is not implemented yet")
}

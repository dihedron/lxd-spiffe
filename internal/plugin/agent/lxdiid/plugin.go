// Package lxdiid implements the agent side of the lxd_iid SPIRE node
// attestor. It will gather the evidence of the LXD instance it runs on and send it to SPIRE Server as the attestation payload.
//
// This is a stub: it accepts an empty configuration and answers every
// attestation with codes.Unimplemented, until the attestation mechanism is
// specified in .specs/lxd-spire-plugins.md.
package lxdiid

import (
	"context"
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

// Config is the plugin_data block of the plugin in the SPIRE Agent
// configuration.
type Config struct{}

// configKeys lists the keys Config accepts: any other key is an error.
var configKeys = []string{}

// Plugin is the agent-side lxd_iid node attestor.
type Plugin struct {
	nodeattestorv1.UnimplementedNodeAttestorServer
	configv1.UnimplementedConfigServer

	mu     sync.RWMutex
	config *Config
}

// New returns an unconfigured Plugin.
func New() *Plugin {
	return &Plugin{}
}

// SetLogger routes the plugin's logs, and those of log/slog, to SPIRE.
func (p *Plugin) SetLogger(logger hclog.Logger) {
	slog.SetDefault(slog.New(logging.NewHandler(logger)))
}

// Configure validates and applies the plugin_data block.
func (p *Plugin) Configure(ctx context.Context, req *configv1.ConfigureRequest) (*configv1.ConfigureResponse, error) {
	var c Config
	if err := config.Decode(req.GetHclConfiguration(), &c, configKeys); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	p.mu.Lock()
	p.config = &c
	p.mu.Unlock()
	return &configv1.ConfigureResponse{}, nil
}

// AidAttestation is not implemented yet.
func (p *Plugin) AidAttestation(stream nodeattestorv1.NodeAttestor_AidAttestationServer) error {
	p.mu.RLock()
	configured := p.config != nil
	p.mu.RUnlock()
	if !configured {
		return status.Error(codes.FailedPrecondition, "not configured")
	}
	return status.Error(codes.Unimplemented, "lxd_iid: attestation is not implemented yet")
}

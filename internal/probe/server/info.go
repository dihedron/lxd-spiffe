package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/url"
	"slices"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// Info is server info (PRS-01, PRS-03).
type Info struct {
	probe.ServerCommand
	ProbeTLS12 bool `long:"probe-tls12" description:"Also try a TLS 1.2 handshake and record whether the server accepts it (PRS-03)."`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *Info) Execute(args []string) error {
	var recorded []string
	if cmd.ProbeTLS12 {
		if u, err := url.Parse(cmd.Endpoint); err != nil || u.Scheme != "https" {
			return exit.New(exit.Usage, errors.New("server info: --probe-tls12 needs an https endpoint"))
		}
		recorded = append(recorded, "--probe-tls12")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "info", recorded)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

func (cmd *Info) run(ctx context.Context, s *session) error {
	resp, err := s.readServer(ctx, &expect.Server)
	if err != nil {
		return err
	}
	if resp.TLSVersion != 0 {
		s.observe("tls.version", tls.VersionName(resp.TLSVersion))
		s.observe("tls.cipher", tls.CipherSuiteName(resp.CipherSuite))
	}
	if srv := s.server; srv != nil {
		s.observe("server.version", srv.Environment.ServerVersion)
		s.observe("server.api_version", srv.APIVersion)
		s.observe("server.auth", srv.Auth)
		s.observe("server.auth_methods", srv.AuthMethods)
		s.observe("server.environment", map[string]any{
			"kernel":           srv.Environment.Kernel,
			"kernel_version":   srv.Environment.KernelVersion,
			"driver":           srv.Environment.Driver,
			"driver_version":   srv.Environment.DriverVersion,
			"server_clustered": srv.Environment.ServerClustered,
			"server_name":      srv.Environment.ServerName,
		})
		s.observe("server.api_extensions", srv.APIExtensions)
		present, missing := []string{}, []string{}
		for _, ext := range expect.RequiredExtensions {
			if slices.Contains(srv.APIExtensions, ext) {
				present = append(present, ext)
			} else {
				missing = append(missing, ext)
			}
		}
		s.observe("extensions.required.present", present)
		s.observe("extensions.required.missing", missing)
	}
	if cmd.ProbeTLS12 {
		return cmd.probeTLS12(ctx, s)
	}
	return nil
}

// probeTLS12 records whether the server accepts a TLS 1.2 handshake. LXD
// documents TLS 1.3 as its minimum, so acceptance is a finding (PRS-03).
func (cmd *Info) probeTLS12(ctx context.Context, s *session) error {
	h, err := lxd.ProbeTLS(ctx, cmd.Endpoint, cmd.Pin, tls.VersionTLS12, tls.VersionTLS12)
	if err != nil {
		return err
	}
	if !h.Accepted {
		s.observe("tls12.accepted", false)
		s.observe("tls12.error", h.Err.Error())
		return nil
	}
	s.finding("tls12.accepted", true)
	s.observe("tls12.version", tls.VersionName(h.Version))
	s.observe("tls12.cipher", tls.CipherSuiteName(h.CipherSuite))
	s.observe("tls12.pin_match", h.PinMatch)
	if !h.PinMatch {
		return &lxd.Error{Kind: lxd.KindConnection, Err: errors.New("the TLS 1.2 handshake presented a certificate that does not match the pin")}
	}
	return nil
}

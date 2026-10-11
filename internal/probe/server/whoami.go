package server

import (
	"context"
	"io"
	"net/http"
	"slices"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// Whoami is server whoami (PRS-02).
type Whoami struct {
	probe.ServerCommand

	stdout io.Writer
}

// Execute runs the command.
func (cmd *Whoami) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "whoami", nil)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

func (cmd *Whoami) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	// without GET /1.0 the extension is unknown: try the call anyway
	if s.server != nil && !slices.Contains(s.server.APIExtensions, expect.IdentityExtension) {
		s.observe("identity.skipped", "the server does not offer the "+expect.IdentityExtension+" extension")
		return nil
	}
	resp, err := s.call(ctx, "identity", lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/auth/identities/current"}, &expect.IdentityCurrent)
	if err != nil {
		return err
	}
	var id api.IdentityInfo
	if !s.decode("identity", resp, &id) {
		return nil
	}
	s.observe("identity.authentication_method", id.AuthenticationMethod)
	s.observe("identity.type", id.Type)
	s.observe("identity.id", id.Identifier)
	s.observe("identity.name", id.Name)
	s.observe("identity.groups", id.Groups)
	s.observe("identity.effective_groups", id.EffectiveGroups)
	s.observe("identity.effective_permissions", id.EffectivePermissions)
	s.observe("identity.fine_grained", id.FineGrained)
	return nil
}

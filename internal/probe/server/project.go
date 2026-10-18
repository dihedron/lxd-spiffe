package server

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// ProjectShow is server project show NAME (PRS-13).
type ProjectShow struct {
	probe.ServerCommand
	Args projectArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// projectArgs is the positional argument of the verbs on one project.
type projectArgs struct {
	Name string `positional-arg-name:"NAME" description:"Project name."`
}

// Execute runs the command.
func (cmd *ProjectShow) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	name := cmd.Args.Name
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "project show", []string{name}, name)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

// run reads the project and records its config, with features.networks on
// its own: whether the project holds its own networks (gate L16).
func (cmd *ProjectShow) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	path := "/1.0/projects/" + url.PathEscape(cmd.Args.Name)
	resp, err := s.call(ctx, "project", lxd.RawRequest{Method: http.MethodGet, Path: path}, &expect.Project)
	if err != nil {
		return err
	}
	s.observe("project.etag", resp.Header.Get("ETag"))
	var p api.Project
	if !s.decode("project", resp, &p) {
		return nil
	}
	s.observe("project.name", p.Name)
	s.observe("project.config", p.Config)
	networks, ok := p.Config["features.networks"]
	if !ok {
		networks = "unset"
	}
	s.observe("project.features.networks", networks)
	return nil
}

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

// InstanceShow is server instance show NAME (PRS-10).
type InstanceShow struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *InstanceShow) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	name := cmd.Args.Name
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "instance show", []string{name}, name)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

// run reads the instance as the attestor does, without recursion, then
// with recursion=1, and records both responses.
func (cmd *InstanceShow) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	path := "/1.0/instances/" + url.PathEscape(cmd.Args.Name)
	plain, err := s.call(ctx, "instance", lxd.RawRequest{Method: http.MethodGet, Path: path}, &expect.Instance)
	if err != nil {
		return err
	}
	full, err := s.call(ctx, "instance-full", lxd.RawRequest{Method: http.MethodGet, Path: path, Query: url.Values{"recursion": {"1"}}}, &expect.InstanceFull)
	if err != nil {
		return err
	}
	// the raw ETag headers, as the attestor will send them back in If-Match
	s.observe("instance.etag", plain.Header.Get("ETag"))
	s.observe("instance-full.etag", full.Header.Get("ETag"))
	s.observe("etag.equal", plain.Header.Get("ETag") == full.Header.Get("ETag"))

	var inst api.Instance
	ok := s.decode("instance", plain, &inst)
	var instFull api.InstanceFull
	if s.decode("instance-full", full, &instFull) && !ok {
		inst, ok = instFull.Instance, true
	}
	if !ok {
		return nil
	}
	s.observe("instance.type", inst.Type)
	s.observe("instance.status", inst.Status)
	s.observe("instance.location", inst.Location)
	s.observe("instance.architecture", inst.Architecture)
	s.observe("instance.config", inst.Config)
	s.observe("instance.devices", inst.Devices)
	s.observe("instance.expanded_devices", inst.ExpandedDevices)
	return nil
}

package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// InstanceList is server instance list (PRS-12).
type InstanceList struct {
	probe.ServerCommand

	stdout io.Writer
}

// InstanceWatch is server instance watch NAME (PRS-11).
type InstanceWatch struct {
	probe.ServerCommand
	For      time.Duration `long:"for" description:"How long to watch." default:"10m"`
	Interval time.Duration `long:"interval" description:"Poll interval." default:"1s"`
	Args     instanceArgs  `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *InstanceList) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "instance list", nil)
	if err != nil {
		return err
	}
	return s.finish(func() error {
		if _, err := s.readServer(ctx, nil); err != nil {
			return err
		}
		resp, err := s.call(ctx, "list", lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/instances", Query: url.Values{"recursion": {"1"}}}, &expect.InstanceList)
		if err != nil {
			return err
		}
		var list []api.Instance
		if !s.decode("list", resp, &list) {
			return nil
		}
		entries := []map[string]string{}
		for _, i := range list {
			entries = append(entries, map[string]string{"name": i.Name, "type": i.Type, "status": i.Status, "location": i.Location})
		}
		s.observe("instances", entries)
		return nil
	}())
}

// watchedState is what instance watch follows (PRS-11).
type watchedState struct {
	Present      bool   `json:"present"`
	UUID         string `json:"uuid,omitempty"`
	Generation   string `json:"uuid.generation,omitempty"`
	CloudInitID  string `json:"cloud-init.instance-id,omitempty"`
	IdmapBase    string `json:"idmap.base,omitempty"`
	IdmapCurrent string `json:"idmap.current,omitempty"`
	IdmapNext    string `json:"idmap.next,omitempty"`
	Location     string `json:"location,omitempty"`
	Status       string `json:"status,omitempty"`
	ETag         string `json:"etag,omitempty"`
}

// fields lists the state's fields by name, for the change log.
func (w watchedState) fields() map[string]string {
	present := "false"
	if w.Present {
		present = "true"
	}
	return map[string]string{
		"present": present, "uuid": w.UUID, "uuid.generation": w.Generation, "cloud-init.instance-id": w.CloudInitID,
		"idmap.base": w.IdmapBase, "idmap.current": w.IdmapCurrent, "idmap.next": w.IdmapNext,
		"location": w.Location, "status": w.Status, "etag": w.ETag,
	}
}

// watchChange is one field's change, seen at At (UTC).
type watchChange struct {
	At    string `json:"at"`
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// fieldOrder fixes the order of a poll's changes.
var fieldOrder = []string{"present", "uuid", "uuid.generation", "cloud-init.instance-id", "idmap.base", "idmap.current", "idmap.next", "location", "status", "etag"}

// Execute runs the command.
func (cmd *InstanceWatch) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.For+cmd.Timeout)
	defer cancel()
	name := cmd.Args.Name
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "instance watch", []string{name, "--for", cmd.For.String(), "--interval", cmd.Interval.String()}, name)
	if err != nil {
		return err
	}
	return s.finish(func() error {
		if _, err := s.readServer(ctx, nil); err != nil {
			return err
		}
		// the polls use the client directly: one artifact each would
		// drown the run
		poll := func() (watchedState, error) {
			resp, err := s.client.Raw(ctx, lxd.RawRequest{Method: http.MethodGet, Path: instancePath(name)})
			if err != nil {
				return watchedState{}, err
			}
			if resp.Status == http.StatusNotFound {
				return watchedState{}, nil
			}
			var inst api.Instance
			if err := lxd.Decode(resp, &inst); err != nil {
				return watchedState{}, err
			}
			c := inst.Config
			return watchedState{
				Present: true, UUID: c["volatile.uuid"], Generation: c["volatile.uuid.generation"],
				CloudInitID: c["volatile.cloud-init.instance-id"], IdmapBase: c["volatile.idmap.base"],
				IdmapCurrent: c["volatile.idmap.current"], IdmapNext: c["volatile.idmap.next"],
				Location: inst.Location, Status: inst.Status, ETag: resp.Header.Get("ETag"),
			}, nil
		}
		start := time.Now()
		last, err := poll()
		if err != nil {
			return err
		}
		s.observe("watch.started", start.UTC().Format(time.RFC3339Nano))
		s.observe("watch.initial", last)
		changes := []watchChange{}
		polls, failures := 0, 0
		for deadline := start.Add(cmd.For); time.Now().Before(deadline); {
			time.Sleep(cmd.Interval)
			now, err := poll()
			polls++
			if err != nil {
				failures++
				continue
			}
			at := time.Now().UTC().Format(time.RFC3339Nano)
			before, after := last.fields(), now.fields()
			for _, f := range fieldOrder {
				if before[f] != after[f] {
					changes = append(changes, watchChange{At: at, Field: f, From: before[f], To: after[f]})
				}
			}
			last = now
		}
		s.observe("watch.polls", polls)
		if failures > 0 {
			s.observe("watch.poll_failures", failures)
		}
		s.observe("watch.changes", changes)
		s.observe("watch.final", last)
		return nil
	}())
}

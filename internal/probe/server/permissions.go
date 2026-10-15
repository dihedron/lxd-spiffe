package server

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// Permissions is server permissions NAME (PRS-40): a fixed matrix of
// read-only calls, each recorded as allowed, denied or hidden, for the
// identity presented. Refusals are its data, not failures.
type Permissions struct {
	probe.ServerCommand
	Args instanceArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// permissionCall is one call of the matrix as recorded.
type permissionCall struct {
	Call    string `json:"call"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
	Verdict string `json:"verdict"`
	Error   string `json:"error,omitempty"`
}

// verdict classes a status: allowed, unauthenticated, denied, hidden or
// another error.
func verdict(status int) string {
	switch {
	case status >= 200 && status <= 299, status == http.StatusSwitchingProtocols:
		return "allowed"
	case status == http.StatusUnauthorized:
		return "unauthenticated"
	case status == http.StatusForbidden:
		return "denied"
	case status == http.StatusNotFound:
		return "hidden"
	}
	return "error " + http.StatusText(status)
}

// Execute runs the command.
func (cmd *Permissions) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	name := cmd.Args.Name
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "permissions", []string{name}, name)
	if err != nil {
		return err
	}
	s.refusalsAreData = true
	return s.finish(func() error {
		if _, err := s.readServer(ctx, nil); err != nil {
			return err
		}
		calls := []struct {
			call, path string
			query      url.Values
		}{
			{"instance", instancePath(name), nil},
			{"state", instancePath(name) + "/state", nil},
			{"files", instancePath(name) + "/files", url.Values{"path": {"/"}}},
			{"operations", "/1.0/operations", nil},
			{"identity", "/1.0/auth/identities/current", nil},
		}
		var matrix []permissionCall
		verdicts := map[string]string{}
		for _, c := range calls {
			resp, err := s.call(ctx, c.call, lxd.RawRequest{Method: http.MethodGet, Path: c.path, Query: c.query}, nil)
			if err != nil {
				return err
			}
			pc := permissionCall{Call: c.call, Method: http.MethodGet, Path: c.path, Status: resp.Status, Verdict: verdict(resp.Status)}
			if env, err := resp.Envelope(); err == nil && env.Error != "" {
				pc.Error = env.Error
			}
			matrix = append(matrix, pc)
			verdicts[c.call] = pc.Verdict
		}
		// the events handshake goes through the SDK's websocket dial; a
		// refused one comes back as a status error
		ev := permissionCall{Call: "events", Method: "GET (websocket)", Path: "/1.0/events"}
		conn, err := s.client.SDK().RawWebsocket("/events")
		if err == nil {
			conn.Close() //nolint:errcheck // the handshake was all we needed
			ev.Status, ev.Verdict = http.StatusSwitchingProtocols, "allowed"
		} else if status, ok := api.StatusErrorMatch(err); ok {
			ev.Status, ev.Verdict, ev.Error = status, verdict(status), err.Error()
		} else {
			ev.Verdict, ev.Error = "error", err.Error()
		}
		matrix = append(matrix, ev)
		verdicts["events"] = ev.Verdict
		s.observe("permissions", verdicts)
		s.observe("permissions.calls", matrix)
		return nil
	}())
}

package guest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// eventsDeadline bounds the plain GET of /1.0/events, a stream (PRF-11).
const eventsDeadline = 2 * time.Second

// DevlxdGet is guest devlxd get PATH (PRF-10).
type DevlxdGet struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"devLXD path, e.g. /1.0/config."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *DevlxdGet) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cmd.Timeout))
	defer cancel()
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "devlxd get", []string{cmd.Args.Path})
	if err != nil {
		return err
	}
	c, err := s.connect(ctx)
	if err != nil {
		return s.finish(err)
	}
	_, err = read(ctx, s, c, cmd.Args.Path)
	return s.finish(err)
}

// DevlxdDump is guest devlxd dump (PRF-11): every known read path, and
// every key /1.0/config lists, one record each.
type DevlxdDump struct {
	probe.GuestCommand

	stdout io.Writer
}

// Execute runs the command.
func (cmd *DevlxdDump) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cmd.Timeout))
	defer cancel()
	paths := []string{"/", "/1.0", "/1.0/config"}
	tail := []string{"/1.0/meta-data", "/1.0/devices", "/1.0/events"}
	for i := 0; i < len(paths); i++ {
		path := paths[i]
		s, err := newSession(&cmd.GuestCommand, cmd.stdout, "devlxd dump", []string{path})
		if err != nil {
			return err
		}
		c, err := s.connect(ctx)
		if err != nil {
			return s.finish(err)
		}
		content, err := read(ctx, s, c, path)
		if err := s.finish(err); err != nil {
			return err
		}
		if path == "/1.0/config" {
			if urls, ok := content.([]any); ok {
				for _, u := range urls {
					if p, ok := u.(string); ok && strings.HasPrefix(p, "/1.0/config/") {
						paths = append(paths, p)
					}
				}
			}
			paths = append(paths, tail...)
		}
	}
	return nil
}

// timeout is the command's timeout, or 30 s when unset (tests).
func timeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 30 * time.Second
	}
	return d
}

// expectationFor returns the expectation of a devLXD path, nil for none.
func expectationFor(path string) *expect.Expectation {
	switch {
	case path == "/":
		return &expect.DevLXDRoot
	case path == "/1.0":
		return &expect.DevLXDServer
	case path == "/1.0/config":
		return &expect.DevLXDConfig
	case strings.HasPrefix(path, "/1.0/config/"):
		return &expect.DevLXDConfigKey
	case path == "/1.0/meta-data":
		return &expect.DevLXDMetadata
	case path == "/1.0/devices":
		return &expect.DevLXDDevices
	}
	return nil
}

// read GETs path into the session's record: the response (PRF-10), its
// content decoded as the probe reads it (PRE-08) and, for an error status,
// its message. /1.0/events, a stream, gets a short deadline whose expiry
// is recorded, not an error. It returns the decoded content, if any.
func read(ctx context.Context, s *session, c *devlxd.Client, path string) (any, error) {
	if path == "/1.0/events" {
		ectx, cancel := context.WithTimeout(ctx, eventsDeadline)
		defer cancel()
		resp, err := s.call(ectx, c, "devlxd", path, nil)
		if lxd.KindOf(err) == lxd.KindTimeout && ctx.Err() == nil {
			s.observe("devlxd.events", "no response within "+eventsDeadline.String()+" (a stream)")
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		s.observe("devlxd.events", "answered: "+http.StatusText(resp.Status))
		return content(s, resp, nil), nil
	}
	resp, err := s.call(ctx, c, "devlxd", path, expectationFor(path))
	if err != nil {
		return nil, err
	}
	var typed any
	switch path {
	case "/1.0":
		typed = &api.DevLXDGet{}
	case "/1.0/devices":
		typed = &map[string]map[string]string{}
	case "/1.0/config", "/":
		typed = &[]string{}
	}
	return content(s, resp, typed), nil
}

// content records the response's content (JSON as JSON, text as text) and
// decodes it into typed when not nil; a decode failure is a finding. An
// error status records its message instead.
func content(s *session, resp *devlxd.Response, typed any) any {
	var v any
	err := resp.Decode(&v)
	if lxd.StatusKind(resp.Status) != lxd.KindNone {
		var e *lxd.Error
		if errors.As(err, &e) && e.Err != nil {
			s.observe("devlxd.error", e.Err.Error())
		}
		return nil
	}
	if err != nil {
		// not JSON: a text value, as devLXD returns a config key or the
		// meta-data (already redacted by call)
		text := string(resp.Body)
		s.observe("devlxd.content", text)
		return text
	}
	s.observe("devlxd.content", v)
	if typed != nil {
		if err := resp.Decode(typed); err != nil {
			s.finding("decode.error", map[string]string{"call": "devlxd", "error": err.Error()})
		}
	}
	return v
}

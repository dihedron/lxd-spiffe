package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// The files API verbs (PRS-30 to PRS-35): what LXD answers for a path in an
// instance, as the attestor's file_pull reads it. A file's content never
// reaches records or artifacts: only its size and hash.

// FileStat is server file stat NAME PATH (PRS-30).
type FileStat struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// FileGet is server file get NAME PATH (PRS-31).
type FileGet struct {
	probe.ServerCommand
	MaxBytes int64    `long:"max-bytes" description:"Read limit, in bytes." default:"1048576"`
	Args     pathArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// FileHead is server file head NAME PATH (PRS-32).
type FileHead struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// FileDir is server file dir NAME PATH (PRS-33).
type FileDir struct {
	probe.ServerCommand
	Args pathArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *FileStat) Execute(args []string) error {
	return runFile(&cmd.ServerCommand, cmd.stdout, "file stat", cmd.Args, nil, func(ctx context.Context, s *session) error {
		resp, err := fileCall(ctx, s, http.MethodGet, cmd.Args, 0, &expect.FileRead)
		if err != nil {
			return err
		}
		observeFile(s, resp, true)
		return nil
	})
}

// Execute runs the command.
func (cmd *FileGet) Execute(args []string) error {
	extra := []string{"--max-bytes", strconv.FormatInt(cmd.MaxBytes, 10)}
	return runFile(&cmd.ServerCommand, cmd.stdout, "file get", cmd.Args, extra, func(ctx context.Context, s *session) error {
		start := time.Now()
		resp, err := fileCall(ctx, s, http.MethodGet, cmd.Args, cmd.MaxBytes, &expect.FileRead)
		if lxd.KindOf(err) == lxd.KindTooLarge {
			// recorded, not a failure (PRS-31, PRS-34)
			s.observe("file.over_limit", map[string]int64{"max_bytes": cmd.MaxBytes})
			return nil
		}
		if err != nil {
			return err
		}
		s.observe("file.latency_ms", time.Since(start).Milliseconds())
		observeFile(s, resp, false)
		return nil
	})
}

// Execute runs the command.
func (cmd *FileHead) Execute(args []string) error {
	return runFile(&cmd.ServerCommand, cmd.stdout, "file head", cmd.Args, nil, func(ctx context.Context, s *session) error {
		resp, err := fileCall(ctx, s, http.MethodHead, cmd.Args, 0, &expect.FileHead)
		if err != nil {
			return err
		}
		headers := lxdHeaders(resp.Header)
		s.observe("file.headers", headers)
		s.observe("file.metadata_without_body", resp.Status == http.StatusOK && len(headers) > 0)
		return nil
	})
}

// Execute runs the command.
func (cmd *FileDir) Execute(args []string) error {
	return runFile(&cmd.ServerCommand, cmd.stdout, "file dir", cmd.Args, nil, func(ctx context.Context, s *session) error {
		resp, err := fileCall(ctx, s, http.MethodGet, cmd.Args, 0, &expect.FileDir)
		if err != nil {
			return err
		}
		s.observe("file.headers", lxdHeaders(resp.Header))
		if lxd.StatusKind(resp.Status) != lxd.KindNone {
			observeError(s, resp)
			return nil
		}
		var entries []string
		if err := lxd.Decode(resp, &entries); err != nil {
			// not a listing: what LXD answers for a non-directory, recorded
			s.observe("file.entries.error", err.Error())
			return nil
		}
		s.observe("file.entries", entries)
		return nil
	})
}

// runFile runs a files API verb: the session, GET /1.0 for the version
// (PRE-02), then fn.
func runFile(opts *probe.ServerCommand, stdout io.Writer, verb string, a pathArgs, extra []string, fn func(context.Context, *session) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	s, err := newSession(ctx, opts, stdout, verb, append([]string{a.Name, a.Path}, extra...), a.Name)
	if err != nil {
		return err
	}
	if _, err := s.readServer(ctx, nil); err != nil {
		return s.finish(err)
	}
	return s.finish(fn(ctx, s))
}

// fileCall sends method on the files API for a.Path.
func fileCall(ctx context.Context, s *session, method string, a pathArgs, maxBytes int64, exp *expect.Expectation) (*lxd.RawResponse, error) {
	return s.call(ctx, "file", lxd.RawRequest{
		Method: method, Path: "/1.0/instances/" + url.PathEscape(a.Name) + "/files",
		Query: url.Values{"path": {a.Path}}, MaxBytes: maxBytes,
	}, exp)
}

// observeFile records a files API answer: its X-LXD-* headers, and the
// body's size and SHA-256, never the body (PRS-35); an error records its
// message.
func observeFile(s *session, resp *lxd.RawResponse, headers bool) {
	if headers {
		s.observe("file.headers", lxdHeaders(resp.Header))
	}
	if lxd.StatusKind(resp.Status) != lxd.KindNone {
		observeError(s, resp)
		return
	}
	sum := sha256.Sum256(resp.Body)
	s.observe("file.size", len(resp.Body))
	s.observe("file.sha256", hex.EncodeToString(sum[:]))
}

// observeError records the message of an error answer.
func observeError(s *session, resp *lxd.RawResponse) {
	if env, err := resp.Envelope(); err == nil {
		s.observe("file.error", env.Error)
		return
	}
	var v any
	if json.Unmarshal(resp.Body, &v) != nil {
		s.observe("file.error", strings.TrimSpace(string(resp.Body)))
	}
}

// lxdHeaders returns every X-LXD-* header with its raw values: no name is
// assumed (PRS-30).
func lxdHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for name, values := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-lxd-") {
			out[name] = values
		}
	}
	return out
}

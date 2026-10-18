package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/canonical/lxd/shared/api"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// session is one invocation of a server verb: its connection, its record
// being written and the exchanges to store as fixtures. Every server verb
// runs as newSession, calls, finish.
type session struct {
	opts   *probe.ServerCommand
	stdout io.Writer
	run    *evidence.Run
	entry  *evidence.Entry
	client *lxd.Client
	start  time.Time
	rec    evidence.Record
	// server is the decoded GET /1.0 of the session, nil until read or if
	// it could not be decoded.
	server *api.Server
	// denied is set by a 403 that was not expected (exit code 4).
	denied bool
	// refusalsAreData keeps 403s from failing the command: for verbs whose
	// result is which calls are refused (server permissions).
	refusalsAreData bool
	// unexpected is set by a finding that is not an expectation.* or
	// decode.* observation.
	unexpected bool

	mu        sync.Mutex
	exchanges []lxd.Exchange
	// quiet keeps exchanges out of --record while set: polls that would
	// drown the fixtures (file stat --wait).
	quiet bool
}

// newSession validates the options, opens the run, connects and reserves
// the record of verb. args are the verb's arguments as recorded; names are
// instance names to alias under --redact-names.
func newSession(ctx context.Context, opts *probe.ServerCommand, stdout io.Writer, verb string, args []string, names ...string) (*session, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	if opts.Endpoint == "" {
		return nil, exit.New(exit.Usage, fmt.Errorf("server %s: --endpoint is required", verb))
	}
	if opts.ExpectDenied && opts.ExpectHidden {
		return nil, exit.New(exit.Usage, fmt.Errorf("server %s: --expect-denied and --expect-hidden exclude each other", verb))
	}
	token, err := bearerToken(opts)
	if err != nil {
		return nil, exit.New(exit.Usage, fmt.Errorf("server %s: %w", verb, err))
	}
	s := &session{opts: opts, stdout: stdout, start: time.Now()}
	s.run, err = opts.OpenRun("server", opts.Endpoint)
	if err != nil {
		return nil, err
	}
	s.run.Sanitizer.AddSecret(token)
	if opts.RedactNames {
		s.run.Sanitizer.RedactName(evidence.KindProject, opts.Project)
		for _, name := range names {
			s.run.Sanitizer.RedactName(evidence.KindInstance, name)
		}
	}
	s.client, err = lxd.Connect(ctx, lxd.Config{
		Endpoint:       opts.Endpoint,
		Pin:            opts.Pin,
		ClientCertFile: opts.ClientCert,
		ClientKeyFile:  opts.ClientKey,
		BearerToken:    token,
		Project:        opts.Project,
		Recorder:       s.record,
	})
	if err != nil {
		if lxd.KindOf(err) == lxd.KindConnection {
			return nil, probe.LXDError(err)
		}
		return nil, exit.New(exit.Usage, fmt.Errorf("server %s: %w", verb, err))
	}
	s.entry, err = s.run.Begin(verb)
	if err != nil {
		return nil, err
	}
	if opts.ExpectDenied {
		args = append(args, "--expect-denied")
	}
	if opts.ExpectHidden {
		args = append(args, "--expect-hidden")
	}
	s.rec = evidence.Record{Time: s.start, Verb: verb, Args: args, Gate: opts.Gate, Cell: opts.Cell}
	return s, nil
}

// bearerToken returns the token of --auth bearer, from --token-file or
// LXD_PROBE_TOKEN (never from an argument, PRN-12); empty for --auth tls.
// LXD's bearer tokens are JWTs, and the SDK refuses anything else when
// connecting: checked here, so that it is a usage error.
func bearerToken(opts *probe.ServerCommand) (string, error) {
	if opts.Auth != "bearer" {
		return "", nil
	}
	token := strings.TrimSpace(os.Getenv("LXD_PROBE_TOKEN"))
	if opts.TokenFile != "" {
		var err error
		if token, err = lxd.ReadSecretFile(opts.TokenFile); err != nil {
			return "", err
		}
	}
	if token == "" {
		return "", errors.New("--auth bearer needs --token-file or LXD_PROBE_TOKEN")
	}
	// the probe presents the token, it never trusts it: LXD verifies its
	// signature on every call, with a key the probe does not have. A token
	// without one is still refused here, since LXD never issues such a
	// token: it is a mistake, better caught before any request.
	parsed, parts, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("the bearer token is not a JWT: %w", err)
	}
	if alg, _ := parsed.Header["alg"].(string); strings.EqualFold(alg, "none") || alg == "" {
		return "", errors.New("the bearer token is unsigned (alg none): LXD never issues such a token")
	}
	if len(parts) != 3 || parts[2] == "" {
		return "", errors.New("the bearer token has no signature: LXD never issues such a token")
	}
	return token, nil
}

// record keeps an exchange for --record.
func (s *session) record(x lxd.Exchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.quiet {
		s.exchanges = append(s.exchanges, x)
	}
}

// setQuiet sets quiet (see the field).
func (s *session) setQuiet(quiet bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quiet = quiet
}

// observe adds an observation to the record.
func (s *session) observe(key string, value any) {
	s.rec.Observations = append(s.rec.Observations, evidence.Observation{Key: key, Value: value})
}

// finding adds an observation that makes the outcome unexpected.
func (s *session) finding(key string, value any) {
	s.observe(key, value)
	s.unexpected = true
}

// call sends a request, stores the response as the artifact name (a
// file's content replaced by its size and hash, PRS-35) and, when
// exp is not nil, checks it against exp, or against exp.Denied() under
// --expect-denied and exp.Hidden() under --expect-hidden. It returns an error only when no response was received.
func (s *session) call(ctx context.Context, name string, req lxd.RawRequest, exp *expect.Expectation) (*lxd.RawResponse, error) {
	resp, err := s.client.Raw(ctx, req)
	if err != nil {
		return nil, err
	}
	body := resp.Body
	if filePath, ok := evidence.FilesAPIPath(req.Path, req.Query.Encode()); ok {
		// a file's content never reaches evidence (PRS-35)
		body = evidence.FileBody(filePath, resp.Status, resp.Header, body, false)
	}
	if _, err := s.entry.ArtifactResponse(name, resp.Status, resp.Header, body); err != nil {
		return nil, err
	}
	s.observe(name+".http_status", resp.Status)
	if exp != nil {
		e := *exp
		switch {
		case s.opts.ExpectDenied:
			e = exp.Denied()
		case s.opts.ExpectHidden:
			e = exp.Hidden()
		}
		s.rec.Expectations = append(s.rec.Expectations, e.Name)
		s.rec.Observations = append(s.rec.Observations, e.Check(resp.Status, resp.Header, resp.Body)...)
	}
	if resp.Status == http.StatusForbidden && !s.opts.ExpectDenied && !s.refusalsAreData {
		s.denied = true
	}
	return resp, nil
}

// decode decodes a successful response into v (PRE-08). An error response
// is recorded as name.error and a decode failure as a decode.error finding;
// both return false.
func (s *session) decode(name string, resp *lxd.RawResponse, v any) bool {
	if lxd.StatusKind(resp.Status) != lxd.KindNone {
		if env, err := resp.Envelope(); err == nil {
			s.observe(name+".error", env.Error)
		}
		return false
	}
	if err := lxd.Decode(resp, v); err != nil {
		s.finding("decode.error", map[string]string{"call": name, "error": err.Error()})
		return false
	}
	return true
}

// readServer reads GET /1.0, which attributes the record to a server
// version and, on a cluster, to the member that answered (PRE-02), and
// checks it against exp when not nil.
func (s *session) readServer(ctx context.Context, exp *expect.Expectation) (*lxd.RawResponse, error) {
	resp, err := s.call(ctx, "server", lxd.RawRequest{Method: http.MethodGet, Path: "/1.0"}, exp)
	if err != nil {
		return nil, err
	}
	var srv api.Server
	if s.decode("server", resp, &srv) {
		s.server = &srv
		s.rec.Server = &evidence.ServerInfo{Version: srv.Environment.ServerVersion, APIExtensionsHash: evidence.HashExtensions(srv.APIExtensions)}
		if srv.Environment.ServerClustered {
			s.rec.Server.Member = srv.Environment.ServerName
		}
	}
	return resp, nil
}

// finish completes and writes the record, stores the fixtures and prints
// the record; it returns the command's error with its exit code. err is
// the error that stopped the verb, if any.
func (s *session) finish(err error) error {
	s.rec.DurationMS = time.Since(s.start).Milliseconds()
	switch {
	case err != nil:
		s.rec.Outcome = evidence.OutcomeError
		s.observe("error", err.Error())
	case s.unexpected || s.deviates():
		s.rec.Outcome = evidence.OutcomeUnexpected
	default:
		s.rec.Outcome = evidence.OutcomeOK
	}
	if ferr := s.writeFixtures(); ferr != nil {
		s.entry.Abort()
		return ferr
	}
	if cerr := s.entry.Commit(&s.rec); cerr != nil {
		s.entry.Abort()
		return cerr
	}
	if perr := s.print(); perr != nil {
		return perr
	}
	switch {
	case err != nil:
		return probe.LXDError(err)
	case s.denied:
		return exit.New(exit.Auth, fmt.Errorf("server %s: access denied (use --expect-denied when the runbook expects it)", s.rec.Verb))
	case s.opts.Strict && s.rec.Outcome == evidence.OutcomeUnexpected:
		return exit.New(exit.Strict, fmt.Errorf("server %s: unexpected outcome", s.rec.Verb))
	}
	return nil
}

// deviates reports whether an observation is an expectation deviation.
func (s *session) deviates() bool {
	for _, o := range s.rec.Observations {
		if strings.HasPrefix(o.Key, "expectation.") {
			return true
		}
	}
	return false
}

// writeFixtures stores the exchanges under --record (PRE-10).
func (s *session) writeFixtures() error {
	if s.opts.Record == "" {
		return nil
	}
	version := ""
	if s.server != nil {
		version = s.server.Environment.ServerVersion
	}
	w := &evidence.FixtureWriter{Dir: s.opts.Record, Sanitizer: s.run.Sanitizer}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.exchanges {
		if err := w.Write(version, s.entry.Number, i+1, x.Method, x.Path, x.Query, x.RequestHeader, x.RequestBody, x.Status, x.ResponseHeader, x.ResponseBody); err != nil {
			return err
		}
	}
	return nil
}

// print writes the committed record (probe.PrintRecord).
func (s *session) print() error {
	return probe.PrintRecord(s.stdout, &s.rec, s.entry.Path(), s.opts.JSON)
}

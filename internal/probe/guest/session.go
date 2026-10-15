package guest

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// session is one invocation of a guest verb: its run and its record being
// written. Every guest verb runs as newSession, observations, finish.
type session struct {
	opts   *probe.GuestCommand
	stdout io.Writer
	run    *evidence.Run
	entry  *evidence.Entry
	start  time.Time
	rec    evidence.Record
	// unexpected makes the outcome unexpected besides expectation.*
	// observations.
	unexpected bool

	mu        sync.Mutex
	exchanges []lxd.Exchange
}

// newSession opens the run of the guest environment and reserves the record
// of verb. args are the verb's arguments as recorded.
func newSession(opts *probe.GuestCommand, stdout io.Writer, verb string, args []string) (*session, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	s := &session{opts: opts, stdout: stdout, start: time.Now()}
	var err error
	s.run, err = opts.OpenRun("guest", "unix://"+opts.Socket)
	if err != nil {
		return nil, err
	}
	s.entry, err = s.run.Begin(verb)
	if err != nil {
		return nil, err
	}
	s.rec = evidence.Record{Time: s.start, Verb: verb, Args: args, Gate: opts.Gate, Cell: opts.Cell}
	return s, nil
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

// connect opens the devLXD socket; every exchange is kept for --record.
func (s *session) connect(ctx context.Context) (*devlxd.Client, error) {
	return devlxd.Connect(ctx, devlxd.Config{Socket: s.opts.Socket, Recorder: func(x lxd.Exchange) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.exchanges = append(s.exchanges, x)
	}})
}

// call GETs path, stores the response as the artifact name (a redacted
// key's bare value redacted, PRF-10) and records its status, format and
// latency, checked against exp when not nil. It returns an error only when
// no response was received.
func (s *session) call(ctx context.Context, c *devlxd.Client, name, path string, exp *expect.Expectation) (*devlxd.Response, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	body := evidence.RedactConfigBody(path, resp.Status, resp.Body)
	if _, err := s.entry.ArtifactResponse(name, resp.Status, resp.Header, body); err != nil {
		return nil, err
	}
	s.observe(name+".http_status", resp.Status)
	s.observe(name+".format", string(resp.Format))
	s.observe(name+".latency_ms", resp.Latency.Milliseconds())
	if exp != nil {
		s.rec.Expectations = append(s.rec.Expectations, exp.Name)
		s.rec.Observations = append(s.rec.Observations, exp.Check(resp.Status, resp.Header, body)...)
	}
	resp.Body = body
	return resp, nil
}

// writeFixtures stores the exchanges under --record, in DIR/devlxd/: the
// guest cannot learn the LXD version (PRE-10).
func (s *session) writeFixtures() error {
	if s.opts.Record == "" {
		return nil
	}
	w := &evidence.FixtureWriter{Dir: s.opts.Record, Sanitizer: s.run.Sanitizer}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.exchanges {
		if err := w.Write("devlxd", s.entry.Number, i+1, x.Method, x.Path, x.Query, x.RequestHeader, x.RequestBody, x.Status, x.ResponseHeader, x.ResponseBody); err != nil {
			return err
		}
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

// finish completes, writes and prints the record; it returns the command's
// error with its exit code. err is the error that stopped the verb, if any.
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
	if perr := probe.PrintRecord(s.stdout, &s.rec, s.entry.Path(), s.opts.JSON); perr != nil {
		return perr
	}
	switch {
	case err != nil:
		return probe.LXDError(fmt.Errorf("guest %s: %w", s.rec.Verb, err))
	case s.opts.Strict && s.rec.Outcome == evidence.OutcomeUnexpected:
		return exit.New(exit.Strict, fmt.Errorf("guest %s: unexpected outcome", s.rec.Verb))
	}
	return nil
}

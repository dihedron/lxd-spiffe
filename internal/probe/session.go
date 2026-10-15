package probe

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// Session is one invocation of a verb that only records what it finds,
// without an LXD connection: the util environment's verbs. (The server and
// guest environments have sessions of their own, with their connections.)
type Session struct {
	opts       *GlobalOptions
	stdout     io.Writer
	entry      *evidence.Entry
	start      time.Time
	rec        evidence.Record
	unexpected bool
}

// NewSession opens the run of env and reserves the record of verb.
func NewSession(opts *GlobalOptions, stdout io.Writer, env, verb string, args []string) (*Session, error) {
	if stdout == nil {
		stdout = os.Stdout
	}
	run, err := opts.OpenRun(env, "")
	if err != nil {
		return nil, err
	}
	entry, err := run.Begin(verb)
	if err != nil {
		return nil, err
	}
	s := &Session{opts: opts, stdout: stdout, entry: entry, start: time.Now()}
	s.rec = evidence.Record{Time: s.start, Verb: verb, Args: args, Gate: opts.Gate, Cell: opts.Cell}
	return s, nil
}

// Observe adds an observation to the record.
func (s *Session) Observe(key string, value any) {
	s.rec.Observations = append(s.rec.Observations, evidence.Observation{Key: key, Value: value})
}

// Finding adds an observation that makes the outcome unexpected.
func (s *Session) Finding(key string, value any) {
	s.Observe(key, value)
	s.unexpected = true
}

// Finish completes, writes and prints the record and returns the
// command's error with its exit code; err stopped the verb, if any.
func (s *Session) Finish(err error) error {
	s.rec.DurationMS = time.Since(s.start).Milliseconds()
	switch {
	case err != nil:
		s.rec.Outcome = evidence.OutcomeError
		s.Observe("error", err.Error())
	case s.unexpected:
		s.rec.Outcome = evidence.OutcomeUnexpected
	default:
		s.rec.Outcome = evidence.OutcomeOK
	}
	if cerr := s.entry.Commit(&s.rec); cerr != nil {
		s.entry.Abort()
		return cerr
	}
	if perr := PrintRecord(s.stdout, &s.rec, s.entry.Path(), s.opts.JSON); perr != nil {
		return perr
	}
	switch {
	case err != nil:
		var e *exit.Error
		if errors.As(err, &e) {
			return err // already has its code
		}
		return exit.New(exit.Internal, fmt.Errorf("%s: %w", s.rec.Verb, err))
	case s.opts.Strict && s.rec.Outcome == evidence.OutcomeUnexpected:
		return exit.New(exit.Strict, fmt.Errorf("%s: unexpected outcome", s.rec.Verb))
	}
	return nil
}

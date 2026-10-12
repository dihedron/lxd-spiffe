package guest

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
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
	if cerr := s.entry.Commit(&s.rec); cerr != nil {
		s.entry.Abort()
		return cerr
	}
	if perr := probe.PrintRecord(s.stdout, &s.rec, s.entry.Path(), s.opts.JSON); perr != nil {
		return perr
	}
	switch {
	case err != nil:
		return exit.New(exit.Internal, fmt.Errorf("guest %s: %w", s.rec.Verb, err))
	case s.opts.Strict && s.rec.Outcome == evidence.OutcomeUnexpected:
		return exit.New(exit.Strict, fmt.Errorf("guest %s: unexpected outcome", s.rec.Verb))
	}
	return nil
}

package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
	"github.com/dihedron/lxd-spiffe/internal/probe/safety"
)

// The config verbs (PRS-20 to PRS-24, gate L14): how LXD takes a write to
// one user.* key, as the attestor's config_push will make it. Writes touch
// only the lab's keys (PRN-02) and print their request line first.

// ConfigGet is server config get NAME KEY (PRS-20); it only reads.
type ConfigGet struct {
	probe.ServerCommand
	Args keyArgs `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// ConfigSet is server config set NAME KEY VALUE (PRS-21).
type ConfigSet struct {
	probe.ServerCommand
	Method             string `long:"method" description:"PATCH with only the key, or PUT of the whole instance." choice:"patch" choice:"put" default:"patch"`                        //nolint:staticcheck // go-flags lists the allowed values as repeated choice tags
	IfMatch            string `long:"if-match" description:"ETag to send: the one read just before, none, or a stale one." choice:"auto" choice:"none" choice:"stale" default:"auto"` //nolint:staticcheck // go-flags lists the allowed values as repeated choice tags
	Wait               bool   `long:"wait" description:"Wait for the operation to complete."`
	ExpectInstanceUUID string `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               struct {
		Name  string `positional-arg-name:"NAME" description:"Instance name."`
		Key   string `positional-arg-name:"KEY" description:"Config key, under user.lxd-probe.*."`
		Value string `positional-arg-name:"VALUE" description:"Value to write."`
	} `positional-args:"yes" required:"yes"`

	stdout, stderr io.Writer
}

// ConfigUnset is server config unset NAME KEY (PRS-22).
type ConfigUnset struct {
	probe.ServerCommand
	Method             string  `long:"method" description:"Way of removing the key." choice:"patch-empty" choice:"patch-null" choice:"put" required:"yes"` //nolint:staticcheck // go-flags lists the allowed values as repeated choice tags
	ExpectInstanceUUID string  `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               keyArgs `positional-args:"yes" required:"yes"`

	stdout, stderr io.Writer
}

// Execute runs the command.
func (cmd *ConfigGet) Execute(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "config get", []string{cmd.Args.Name, cmd.Args.Key}, cmd.Args.Name)
	if err != nil {
		return err
	}
	return s.finish(func() error {
		if _, err := s.readServer(ctx, nil); err != nil {
			return err
		}
		inst, _, err := readInstance(ctx, s, cmd.Args.Name, "instance")
		if err != nil {
			return err
		}
		value, ok := inst.Config[cmd.Args.Key]
		if ok {
			// keyed by the config key, so that the sanitizer redacts it as such
			s.observe("config", map[string]string{cmd.Args.Key: value})
		}
		s.observe("config.present", ok)
		return nil
	}())
}

// Execute runs the command.
func (cmd *ConfigSet) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	if err := safety.Key(cmd.Args.Key, cmd.AllowSpireKeys); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	recorded := []string{cmd.Args.Name, cmd.Args.Key, cmd.Args.Value, "--method", cmd.Method, "--if-match", cmd.IfMatch}
	if cmd.Wait {
		recorded = append(recorded, "--wait")
	}
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "config set", recorded, cmd.Args.Name)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

func (cmd *ConfigSet) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	name, key := cmd.Args.Name, cmd.Args.Key
	before, etag, err := readInstance(ctx, s, name, "before")
	if err != nil {
		return err
	}
	if err := guardUUID(before, cmd.ExpectInstanceUUID); err != nil {
		return err
	}
	sendETag := etag
	stale := false
	switch cmd.IfMatch {
	case "none":
		sendETag = ""
	case "stale":
		// a first write with the ETag read, which makes it outdated
		pre := cmd.Args.Value + "~pre"
		if _, err := write(ctx, s, cmd.stderr, "pre", http.MethodPatch, name, patchBody(key, &pre), etag, true, writeExpectation(http.MethodPatch, false)); err != nil {
			return err
		}
		stale = true
	}
	value := cmd.Args.Value
	method, body := http.MethodPatch, patchBody(key, &value)
	if cmd.Method == "put" {
		method, body = http.MethodPut, putBody(before, key, &value)
	}
	if _, err := write(ctx, s, cmd.stderr, "write", method, name, body, sendETag, cmd.Wait, writeExpectation(method, stale)); err != nil {
		return err
	}
	return observeAfter(ctx, s, name, key, before)
}

// Execute runs the command.
func (cmd *ConfigUnset) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	if err := safety.Key(cmd.Args.Key, cmd.AllowSpireKeys); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "config unset", []string{cmd.Args.Name, cmd.Args.Key, "--method", cmd.Method}, cmd.Args.Name)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

func (cmd *ConfigUnset) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	name, key := cmd.Args.Name, cmd.Args.Key
	before, etag, err := readInstance(ctx, s, name, "before")
	if err != nil {
		return err
	}
	if err := guardUUID(before, cmd.ExpectInstanceUUID); err != nil {
		return err
	}
	_, had := before.Config[key]
	s.observe("unset.present_before", had)
	var method string
	var body []byte
	switch cmd.Method {
	case "patch-empty":
		empty := ""
		method, body = http.MethodPatch, patchBody(key, &empty)
	case "patch-null":
		method, body = http.MethodPatch, patchBody(key, nil)
	default:
		method, body = http.MethodPut, putBody(before, key, nil)
	}
	if _, err := write(ctx, s, cmd.stderr, "write", method, name, body, etag, true, writeExpectation(method, false)); err != nil {
		return err
	}
	after, _, err := readInstance(ctx, s, name, "after")
	if err != nil {
		return err
	}
	value, present := after.Config[key]
	result := "gone"
	switch {
	case present && value == "":
		result = "empty"
	case present && value == before.Config[key]:
		result = "unchanged"
	case present:
		result = "changed"
	}
	s.observe("unset.result", result)
	return nil
}

// writeExpectation returns the expectation of a write: PATCH or PUT, or
// their 412 when the ETag is stale.
func writeExpectation(method string, stale bool) *expect.Expectation {
	e := expect.InstancePatch
	if method == http.MethodPut {
		e = expect.InstancePut
	}
	if stale {
		e = e.Precondition()
	}
	return &e
}

// readInstance reads GET /1.0/instances/NAME as call, decodes it and returns
// it with its ETag.
func readInstance(ctx context.Context, s *session, name, call string) (*api.Instance, string, error) {
	resp, err := s.call(ctx, call, lxd.RawRequest{Method: http.MethodGet, Path: instancePath(name)}, &expect.Instance)
	if err != nil {
		return nil, "", err
	}
	var inst api.Instance
	if !s.decode(call, resp, &inst) {
		return nil, "", fmt.Errorf("instance %s could not be read (%d)", name, resp.Status)
	}
	return &inst, resp.Header.Get("ETag"), nil
}

func instancePath(name string) string {
	return "/1.0/instances/" + url.PathEscape(name)
}

// guardUUID refuses (exit 5) when the instance is not the expected one
// (PRS-24).
func guardUUID(inst *api.Instance, want string) error {
	if want == "" || inst.Config["volatile.uuid"] == want {
		return nil
	}
	return exit.New(exit.Refused, fmt.Errorf("write refused: %s has volatile.uuid %q, not %q", inst.Name, inst.Config["volatile.uuid"], want))
}

// patchBody is a PATCH of one key; a nil value is a JSON null.
func patchBody(key string, value *string) []byte {
	data, _ := json.Marshal(map[string]map[string]*string{"config": {key: value}})
	return data
}

// putBody is the instance's writable fields with key set, or removed when
// value is nil.
func putBody(inst *api.Instance, key string, value *string) []byte {
	put := inst.Writable()
	put.Config = maps.Clone(put.Config)
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	if value == nil {
		delete(put.Config, key)
	} else {
		put.Config[key] = *value
	}
	data, _ := json.Marshal(put)
	return data
}

// write prints the request line (PRS-24), sends the write as call, records
// its duration and, with wait, waits on its operation.
func write(ctx context.Context, s *session, stderr io.Writer, call, method, name string, body []byte, etag string, wait bool, exp *expect.Expectation) (*lxd.RawResponse, error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	line := fmt.Sprintf("%s %s?project=%s", method, instancePath(name), s.opts.Project)
	if etag != "" {
		line += " If-Match: " + etag
	}
	fmt.Fprintln(stderr, line) //nolint:errcheck // informational
	start := time.Now()
	resp, err := s.call(ctx, call, lxd.RawRequest{Method: method, Path: instancePath(name), Body: body, ETag: etag}, exp)
	if err != nil {
		return nil, err
	}
	s.observe(call+".duration_ms", time.Since(start).Milliseconds())
	// when the server answered the write, for the delays of gate L2
	s.observe(call+".completed_at", time.Now().UTC().Format(time.RFC3339Nano))
	env, err := resp.Envelope()
	if err != nil || lxd.StatusKind(resp.Status) != lxd.KindNone {
		if err == nil {
			s.observe(call+".error", env.Error)
		}
		return resp, nil
	}
	if env.Operation != "" {
		s.observe(call+".operation", env.Operation)
		if wait {
			if err := waitOperation(ctx, s, env.Operation); err != nil {
				return nil, err
			}
		}
	}
	return resp, nil
}

// waitOperation waits on an operation and records its outcome.
func waitOperation(ctx context.Context, s *session, op string) error {
	start := time.Now()
	resp, err := s.call(ctx, "operation", lxd.RawRequest{Method: http.MethodGet, Path: op + "/wait"}, &expect.OperationWait)
	if err != nil {
		return err
	}
	s.observe("operation.duration_ms", time.Since(start).Milliseconds())
	var o api.Operation
	if s.decode("operation", resp, &o) {
		s.observe("operation.status", o.Status)
		if o.Err != "" {
			s.observe("operation.err", o.Err)
		}
	}
	return nil
}

// observeAfter reads the instance again and records the key's value and
// whether the write restarted the instance.
func observeAfter(ctx context.Context, s *session, name, key string, before *api.Instance) error {
	after, _, err := readInstance(ctx, s, name, "after")
	if err != nil {
		return err
	}
	if value, ok := after.Config[key]; ok {
		s.observe("config.after", map[string]string{key: value})
	} else {
		s.observe("config.after", map[string]string{})
	}
	s.observe("instance.status_before", before.Status)
	s.observe("instance.status_after", after.Status)
	s.observe("instance.restarted", before.Status != after.Status || !before.LastUsedAt.Equal(after.LastUsedAt))
	return nil
}

// ConfigRace is server config race NAME KEY --writers N (PRS-23).
type ConfigRace struct {
	probe.ServerCommand
	Writers            int     `long:"writers" description:"Number of concurrent writers." default:"8"`
	Retries            int     `long:"retries" description:"Retry budget per writer on 412." default:"10"`
	ExpectInstanceUUID string  `long:"expect-instance-uuid" description:"Refuse to write unless the instance's volatile.uuid is this."`
	Args               keyArgs `positional-args:"yes" required:"yes"`

	stdout, stderr io.Writer
}

// raceEvent is one write of a racing writer.
type raceEvent struct {
	Writer  int    `json:"writer"`
	Attempt int    `json:"attempt"`
	Status  int    `json:"status"`
	AtMS    int64  `json:"at_ms"`
	Error   string `json:"error,omitempty"`
}

// Execute runs the command.
func (cmd *ConfigRace) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	if err := safety.Key(cmd.Args.Key, cmd.AllowSpireKeys); err != nil {
		return err
	}
	if cmd.Writers < 1 || cmd.Retries < 0 {
		return exit.New(exit.Usage, fmt.Errorf("server config race: --writers must be at least 1 and --retries not negative"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmd.Timeout)
	defer cancel()
	recorded := []string{cmd.Args.Name, cmd.Args.Key, "--writers", fmt.Sprint(cmd.Writers), "--retries", fmt.Sprint(cmd.Retries)}
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "config race", recorded, cmd.Args.Name)
	if err != nil {
		return err
	}
	return s.finish(cmd.run(ctx, s))
}

func (cmd *ConfigRace) run(ctx context.Context, s *session) error {
	if _, err := s.readServer(ctx, nil); err != nil {
		return err
	}
	name, key := cmd.Args.Name, cmd.Args.Key
	before, _, err := readInstance(ctx, s, name, "before")
	if err != nil {
		return err
	}
	if err := guardUUID(before, cmd.ExpectInstanceUUID); err != nil {
		return err
	}
	stderr := cmd.stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "%d writers: GET then PATCH %s?project=%s If-Match: <ETag read>\n", cmd.Writers, instancePath(name), s.opts.Project) //nolint:errcheck // informational

	// the writers call the client directly: the session's record is not
	// for concurrent use; their events are recorded afterwards
	var mu sync.Mutex
	var events []raceEvent
	start := make(chan struct{})
	begin := time.Now()
	var wg sync.WaitGroup
	for w := range cmd.Writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			value := fmt.Sprintf("w%d", w)
			for attempt := 0; attempt <= cmd.Retries; attempt++ {
				ev := raceEvent{Writer: w, Attempt: attempt}
				status, err := raceWrite(ctx, s, name, key, value)
				ev.Status, ev.AtMS = status, time.Since(begin).Milliseconds()
				if err != nil {
					ev.Error = err.Error()
				}
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
				if status != http.StatusPreconditionFailed {
					return
				}
				time.Sleep(time.Duration(10+rand.IntN(91)) * time.Millisecond) //nolint:gosec // G404: retry jitter, not a secret
			}
		}()
	}
	close(start)
	wg.Wait()

	slices.SortFunc(events, func(a, b raceEvent) int { return cmp.Compare(a.AtMS, b.AtMS) })
	s.observe("race.events", events)
	ok, conflicts, exhausted, last := 0, 0, 0, -1
	perWriter := map[int][]raceEvent{}
	for _, ev := range events {
		perWriter[ev.Writer] = append(perWriter[ev.Writer], ev)
		switch ev.Status {
		case http.StatusOK:
			ok++
			last = ev.Writer
		case http.StatusPreconditionFailed:
			conflicts++
		}
	}
	for _, evs := range perWriter {
		if evs[len(evs)-1].Status == http.StatusPreconditionFailed {
			exhausted++
		}
	}
	s.observe("race.totals", map[string]int{"200": ok, "412": conflicts, "exhausted": exhausted, "writes": len(events)})
	after, _, err := readInstance(ctx, s, name, "after")
	if err != nil {
		return err
	}
	final := after.Config[key]
	s.observe("race.final", map[string]string{key: final})
	s.observe("race.consistent", last >= 0 && final == fmt.Sprintf("w%d", last))
	return nil
}

// raceWrite reads the instance's ETag and PATCHes the key with If-Match.
func raceWrite(ctx context.Context, s *session, name, key, value string) (int, error) {
	get, err := s.client.Raw(ctx, lxd.RawRequest{Method: http.MethodGet, Path: instancePath(name)})
	if err != nil {
		return 0, err
	}
	resp, err := s.client.Raw(ctx, lxd.RawRequest{Method: http.MethodPatch, Path: instancePath(name), Body: patchBody(key, &value), ETag: get.Header.Get("ETag")})
	if err != nil {
		return 0, err
	}
	return resp.Status, nil
}

package guest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// pollInterval is how often watch reads the key (PRF-12).
const pollInterval = 100 * time.Millisecond

// maxEvents bounds the events a watch records.
const maxEvents = 200

// DevlxdWatch is guest devlxd watch KEY (PRF-12): how and when a change of
// the key reaches the guest, by polling and through the event stream.
type DevlxdWatch struct {
	probe.GuestCommand
	For  time.Duration `long:"for" description:"How long to watch." default:"60s"`
	Args struct {
		Key string `positional-arg-name:"KEY" description:"Config key to watch."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// watchValue is the key's state: present, and its value (redacted by the
// key's name).
type watchValue struct {
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
}

// watchChange is a change of the key, seen at At (UTC) by Source.
type watchChange struct {
	At     string `json:"at"`
	Source string `json:"source"`
	watchValue
}

// watchEvent is an event received, its metadata redacted.
type watchEvent struct {
	At       string          `json:"at"`
	Type     string          `json:"type"`
	Sent     string          `json:"sent"`
	Metadata json.RawMessage `json:"metadata"`
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Execute runs the command.
func (cmd *DevlxdWatch) Execute(args []string) error {
	key := cmd.Args.Key
	ctx, cancel := context.WithTimeout(context.Background(), cmd.For+timeout(cmd.Timeout))
	defer cancel()
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "devlxd watch", []string{key, "--for", cmd.For.String()})
	if err != nil {
		return err
	}
	c, err := s.connect(ctx)
	if err != nil {
		return s.finish(err)
	}
	redact := func(v string) string {
		return string(evidence.RedactConfigBody("/1.0/config/"+key, http.StatusOK, []byte(v)))
	}
	read := func() (watchValue, bool) {
		resp, err := c.Get(ctx, "/1.0/config/"+key)
		if err != nil {
			return watchValue{}, false
		}
		var v string
		switch {
		case resp.Status == http.StatusOK && resp.Decode(&v) == nil:
			return watchValue{Present: true, Value: redact(v)}, true
		case resp.Status == http.StatusOK:
			return watchValue{Present: true, Value: redact(string(resp.Body))}, true
		case resp.Status == http.StatusNotFound:
			return watchValue{}, true
		}
		return watchValue{}, false
	}

	start := time.Now()
	s.observe("watch.key", key)
	s.observe("watch.started", utc(start))
	initial, _ := read()
	s.observe("watch.initial", initial)

	var mu sync.Mutex
	changes := []watchChange{}
	events := []watchEvent{}
	lastEvent := initial
	stop, eerr := c.Events(func(e api.Event) {
		now := time.Now()
		var md struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		_ = json.Unmarshal(e.Metadata, &md)
		mu.Lock()
		defer mu.Unlock()
		if len(events) < maxEvents {
			events = append(events, watchEvent{At: utc(now), Type: e.Type, Sent: utc(e.Timestamp), Metadata: redactMetadata(e.Metadata)})
		}
		if e.Type == "config" && md.Key == key {
			v := watchValue{Present: md.Value != "", Value: redact(md.Value)}
			if v != lastEvent {
				changes = append(changes, watchChange{At: utc(now), Source: "event", watchValue: v})
				lastEvent = v
			}
		}
	})
	s.observe("watch.events_available", eerr == nil)
	if eerr != nil {
		s.observe("watch.events_error", eerr.Error())
	} else {
		defer stop()
	}

	last, polls, failed := initial, 0, 0
	deadline := start.Add(cmd.For)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		v, ok := read()
		polls++
		if !ok {
			failed++
			continue
		}
		if v != last {
			mu.Lock()
			changes = append(changes, watchChange{At: utc(time.Now()), Source: "poll", watchValue: v})
			mu.Unlock()
			last = v
		}
	}
	mu.Lock()
	defer mu.Unlock()
	s.observe("watch.polls", polls)
	if failed > 0 {
		s.observe("watch.poll_failures", failed)
	}
	s.observe("watch.changes", changes)
	s.observe("watch.events", events)
	return s.finish(nil)
}

// redactMetadata redacts the values of a config event whose key is
// redacted: they sit under value and old_value, where the JSON sanitizer
// cannot tell them from any other field.
func redactMetadata(raw json.RawMessage) json.RawMessage {
	var md map[string]any
	if json.Unmarshal(raw, &md) != nil {
		return raw
	}
	key, _ := md["key"].(string)
	if key == "" {
		return raw
	}
	for _, field := range []string{"value", "old_value"} {
		if v, ok := md[field].(string); ok {
			md[field] = string(evidence.RedactConfigBody("/1.0/config/"+key, http.StatusOK, []byte(v)))
		}
	}
	out, err := json.Marshal(md)
	if err != nil {
		return raw
	}
	return out
}

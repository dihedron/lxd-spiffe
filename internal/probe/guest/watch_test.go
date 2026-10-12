package guest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// change is a watch.changes entry as recorded.
type change struct {
	At      string `json:"at"`
	Source  string `json:"source"`
	Present bool   `json:"present"`
	Value   string `json:"value"`
}

func watch(t *testing.T, fake *fakelxd.DevLXD, key string, d time.Duration, during func()) evidence.Record {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &DevlxdWatch{GuestCommand: guestOptions(t, fake.Socket), For: d, stdout: out}
	cmd.Args.Key = key
	done := make(chan error, 1)
	go func() { done <- cmd.Execute(nil) }()
	during()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return records(t, out)[0]
}

func changes(t *testing.T, rec evidence.Record) []change {
	t.Helper()
	var out []change
	if err := json2(value(t, rec, "watch.changes"), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDevlxdWatch(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.SetConfig("user.lxd-probe.t1", "v0")
	rec := watch(t, fake, "user.lxd-probe.t1", 1500*time.Millisecond, func() {
		time.Sleep(400 * time.Millisecond)
		fake.SetConfig("user.lxd-probe.t1", "v1")
		time.Sleep(400 * time.Millisecond)
		fake.SetConfig("user.lxd-probe.t1", "v2")
	})
	if rec.Verb != "devlxd watch" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s %v", rec.Verb, rec.Outcome, rec.Observations)
	}
	if got := jsonOf(value(t, rec, "watch.initial")); got != `{"present":true,"value":"v0"}` {
		t.Errorf("watch.initial = %s", got)
	}
	if value(t, rec, "watch.events_available") != true {
		t.Errorf("events not available: %v", rec.Observations)
	}
	var bySource = map[string][]string{}
	for _, c := range changes(t, rec) {
		bySource[c.Source] = append(bySource[c.Source], c.Value)
		if _, err := time.Parse(time.RFC3339Nano, c.At); err != nil || !strings.HasSuffix(c.At, "Z") {
			t.Errorf("timestamp %q is not UTC RFC 3339: %v", c.At, err)
		}
	}
	for _, source := range []string{"poll", "event"} {
		if got := strings.Join(bySource[source], ","); got != "v1,v2" {
			t.Errorf("%s changes %q; want v1,v2", source, got)
		}
	}
	if n, _ := value(t, rec, "watch.polls").(float64); n < 8 {
		t.Errorf("%v polls in 1.5 s; want about 15", n)
	}
}

func TestDevlxdWatchRedacts(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	rec := watch(t, fake, "cloud-init.user-data", time.Second, func() {
		time.Sleep(300 * time.Millisecond)
		fake.SetConfig("cloud-init.user-data", secret)
	})
	cs := changes(t, rec)
	if len(cs) == 0 {
		t.Fatalf("no change seen: %v", rec.Observations)
	}
	for _, c := range cs {
		if c.Value != evidence.Redacted {
			t.Errorf("%s change value %q; want it redacted", c.Source, c.Value)
		}
	}
	if strings.Contains(jsonOf(rec.Observations), "hunter2") {
		t.Error("the value reached the record")
	}
}

func TestDevlxdWatchWithoutEvents(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.NoEvents = true
	rec := watch(t, fake, "user.lxd-probe.t1", 500*time.Millisecond, func() {})
	if value(t, rec, "watch.events_available") != false || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("without events: %s %v", rec.Outcome, rec.Observations)
	}
	if got := jsonOf(value(t, rec, "watch.initial")); got != `{"present":false}` {
		t.Errorf("watch.initial = %s", got)
	}
}

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const uuid = "8d4e1c2a-0000-4000-8000-000000000001"

func configFake(t *testing.T) *fakelxd.Server {
	t.Helper()
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", map[string]string{
		"volatile.uuid": uuid, "user.lxd-probe.a": "old", "user.secret": "hunter2", "limits.cpu": "1",
	})
	return fake
}

func writeOpts(t *testing.T, fake *fakelxd.Server) probe.ServerCommand {
	t.Helper()
	opts := options(t, fake)
	opts.AllowWrite, opts.Lab = true, true
	return opts
}

// writes returns the PATCH and PUT requests the fake received.
func writes(fake *fakelxd.Server) []fakelxd.Request {
	var out []fakelxd.Request
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPatch || r.Method == http.MethodPut {
			out = append(out, r)
		}
	}
	return out
}

// configOf reads the instance's config as the fake holds it.
func configOf(t *testing.T, fake *fakelxd.Server) map[string]string {
	t.Helper()
	cfg, ok := fake.Config("default", "c1")
	if !ok {
		t.Fatal("no instance c1")
	}
	return cfg
}

func set(t *testing.T, opts probe.ServerCommand, key, value, method, ifMatch string, wait bool, uuid string) (*bytes.Buffer, error) {
	t.Helper()
	stderr := &bytes.Buffer{}
	cmd := &ConfigSet{ServerCommand: opts, Method: method, IfMatch: ifMatch, Wait: wait, ExpectInstanceUUID: uuid, stdout: &bytes.Buffer{}, stderr: stderr}
	cmd.Args.Name, cmd.Args.Key, cmd.Args.Value = "c1", key, value
	return stderr, cmd.Execute(nil)
}

func TestConfigGet(t *testing.T) {
	fake := configFake(t)
	opts := options(t, fake) // no write flags: get only reads
	for i, key := range []string{"user.lxd-probe.a", "user.secret", "user.lxd-probe.missing"} {
		cmd := &ConfigGet{ServerCommand: opts, stdout: &bytes.Buffer{}}
		cmd.Args.Name, cmd.Args.Key = "c1", key
		if err := cmd.Execute(nil); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		rec := record(t, opts, i+1)
		present := observation(t, rec, "config.present")
		switch key {
		case "user.lxd-probe.a":
			cfg, _ := observation(t, rec, "config").(map[string]any)
			if cfg[key] != "old" || present != true {
				t.Errorf("%s: %v %v", key, cfg, present)
			}
		case "user.secret":
			cfg, _ := observation(t, rec, "config").(map[string]any)
			if cfg[key] != evidence.Redacted {
				t.Errorf("%s: %v (a user.* value is redacted)", key, cfg)
			}
		default:
			if present != false {
				t.Errorf("%s: present %v", key, present)
			}
		}
	}
	if len(writes(fake)) != 0 {
		t.Error("get wrote")
	}
}

func TestConfigSetRefusals(t *testing.T) {
	fake := configFake(t)
	noFlags := options(t, fake)
	if _, err := set(t, noFlags, "user.lxd-probe.a", "v", "patch", "auto", false, ""); exit.Code(err) != exit.Refused {
		t.Errorf("without --allow-write --lab: exit %d (%v)", exit.Code(err), err)
	}
	opts := writeOpts(t, fake)
	if _, err := set(t, opts, "limits.cpu", "2", "patch", "auto", false, ""); exit.Code(err) != exit.Refused {
		t.Errorf("a key outside the lab's: exit %d (%v)", exit.Code(err), err)
	}
	// the wrong target: refused after reading, before writing (PRS-24)
	if _, err := set(t, opts, "user.lxd-probe.a", "v", "patch", "auto", false, "another-uuid"); exit.Code(err) != exit.Refused {
		t.Errorf("--expect-instance-uuid mismatch: exit %d (%v)", exit.Code(err), err)
	}
	if w := writes(fake); len(w) != 0 {
		t.Errorf("a refused set wrote: %v", w)
	}
}

func TestConfigSetPatch(t *testing.T) {
	fake := configFake(t)
	opts := writeOpts(t, fake)
	stderr, err := set(t, opts, "user.lxd-probe.a", "v1", "patch", "auto", true, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if got := configOf(t, fake)["user.lxd-probe.a"]; got != "v1" {
		t.Errorf("value %q", got)
	}
	w := writes(fake)
	if len(w) != 1 || w[0].Method != http.MethodPatch || w[0].Header.Get("If-Match") == "" {
		t.Fatalf("writes %+v", w)
	}
	var body map[string]map[string]string
	if json.Unmarshal(w[0].Body, &body) != nil || len(body["config"]) != 1 || body["config"]["user.lxd-probe.a"] != "v1" {
		t.Errorf("PATCH body %s; want only the key", w[0].Body)
	}
	// the request line is printed before acting (PRS-24)
	if !strings.Contains(stderr.String(), "PATCH /1.0/instances/c1") {
		t.Errorf("request line: %q", stderr.String())
	}
	rec := record(t, opts, 1)
	if rec.Verb != "config set" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s %v", rec.Verb, rec.Outcome, rec.Observations)
	}
	// PATCH is synchronous: no operation to wait for
	for _, key := range []string{"write.http_status", "write.duration_ms", "instance.restarted"} {
		observation(t, rec, key)
	}
	done, _ := observation(t, rec, "write.completed_at").(string)
	if at, err := time.Parse(time.RFC3339Nano, done); err != nil || at.Location() != time.UTC {
		t.Errorf("write.completed_at %q: %v", done, err)
	}
	if observation(t, rec, "instance.restarted") != false {
		t.Errorf("restart: %v", observation(t, rec, "instance.restarted"))
	}
}

func TestConfigSetStaleAndNone(t *testing.T) {
	fake := configFake(t)
	opts := writeOpts(t, fake)
	// stale: a first write with the ETag read, then the request with it,
	// now outdated: 412, as expected
	if _, err := set(t, opts, "user.lxd-probe.a", "v2", "patch", "stale", false, ""); err != nil {
		t.Fatal(err)
	}
	w := writes(fake)
	if len(w) != 2 || w[0].Header.Get("If-Match") != w[1].Header.Get("If-Match") {
		t.Fatalf("stale writes %+v", w)
	}
	if got := configOf(t, fake)["user.lxd-probe.a"]; got != "v2~pre" {
		t.Errorf("value %q; want the preliminary write's", got)
	}
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeOK || observation(t, rec, "write.http_status") != float64(http.StatusPreconditionFailed) {
		t.Errorf("stale: %s %v", rec.Outcome, rec.Observations)
	}

	// none: no If-Match at all
	if _, err := set(t, opts, "user.lxd-probe.a", "v3", "patch", "none", false, ""); err != nil {
		t.Fatal(err)
	}
	w = writes(fake)
	if last := w[len(w)-1]; last.Header.Get("If-Match") != "" {
		t.Errorf("--if-match none sent %q", last.Header.Get("If-Match"))
	}
}

func TestConfigSetPut(t *testing.T) {
	fake := configFake(t)
	opts := writeOpts(t, fake)
	if _, err := set(t, opts, "user.lxd-probe.a", "v4", "put", "auto", true, ""); err != nil {
		t.Fatal(err)
	}
	w := writes(fake)
	if len(w) != 1 || w[0].Method != http.MethodPut {
		t.Fatalf("writes %+v", w)
	}
	cfg := configOf(t, fake)
	if cfg["user.lxd-probe.a"] != "v4" || cfg["limits.cpu"] != "1" || cfg["volatile.uuid"] != uuid {
		t.Errorf("after PUT: %v; want the key set and the rest kept", cfg)
	}
	// PUT answers with an operation, which --wait waits for
	rec := record(t, opts, 1)
	if rec.Outcome != evidence.OutcomeOK || observation(t, rec, "operation.status") != "Success" {
		t.Errorf("put: %s %v", rec.Outcome, rec.Observations)
	}
}

func TestConfigUnset(t *testing.T) {
	for _, method := range []string{"patch-empty", "patch-null", "put"} {
		t.Run(method, func(t *testing.T) {
			fake := configFake(t)
			opts := writeOpts(t, fake)
			cmd := &ConfigUnset{ServerCommand: opts, Method: method, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
			cmd.Args.Name, cmd.Args.Key = "c1", "user.lxd-probe.a"
			if err := cmd.Execute(nil); err != nil {
				t.Fatal(err)
			}
			rec := record(t, opts, 1)
			// the fake removes the key every way: the lab tells what LXD does
			if observation(t, rec, "unset.result") != "gone" || rec.Outcome != evidence.OutcomeOK {
				t.Errorf("%s: %s %v", method, rec.Outcome, rec.Observations)
			}
			if _, ok := configOf(t, fake)["user.lxd-probe.a"]; ok {
				t.Error("still there")
			}
			if !slices.Contains(rec.Args, "--method") {
				t.Errorf("args %v", rec.Args)
			}
		})
	}
}

func TestConfigRace(t *testing.T) {
	fake := configFake(t)
	opts := writeOpts(t, fake)
	cmd := &ConfigRace{ServerCommand: opts, Writers: 8, Retries: 10, ExpectInstanceUUID: uuid, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Key = "c1", "user.lxd-probe.r"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Verb != "config race" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s %v", rec.Verb, rec.Outcome, rec.Observations)
	}
	totals, _ := observation(t, rec, "race.totals").(map[string]any)
	if totals["200"] != float64(8) || totals["exhausted"] != float64(0) {
		t.Errorf("totals %v; want every writer through within its budget", totals)
	}
	events, _ := observation(t, rec, "race.events").([]any)
	if len(events) < 8 {
		t.Errorf("events %d", len(events))
	}
	if observation(t, rec, "race.consistent") != true {
		t.Errorf("final value is not the last successful writer's: %v", rec.Observations)
	}
	final := configOf(t, fake)["user.lxd-probe.r"]
	if !strings.HasPrefix(final, "w") {
		t.Errorf("final value %q", final)
	}
	// every write carried an If-Match
	for _, w := range writes(fake) {
		if w.Header.Get("If-Match") == "" {
			t.Errorf("a write without If-Match: %+v", w)
		}
	}

	// refusals, before any write
	refused := configFake(t)
	cmd = &ConfigRace{ServerCommand: options(t, refused), Writers: 2, Retries: 1, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Key = "c1", "user.lxd-probe.r"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Refused {
		t.Errorf("without write flags: exit %d (%v)", exit.Code(err), err)
	}
	cmd = &ConfigRace{ServerCommand: writeOpts(t, refused), Writers: 2, Retries: 1, ExpectInstanceUUID: "other", stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cmd.Args.Name, cmd.Args.Key = "c1", "user.lxd-probe.r"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Refused {
		t.Errorf("wrong instance: exit %d (%v)", exit.Code(err), err)
	}
	if len(writes(refused)) != 0 {
		t.Error("a refused race wrote")
	}
}

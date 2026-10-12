package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

func TestInstanceList(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", nil)
	fake.AddInstance("default", "v1", nil)
	fake.AddInstance("restricted", "c1", nil)
	opts := options(t, fake)
	cmd := &InstanceList{ServerCommand: opts, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	list, _ := observation(t, rec, "instances").([]any)
	if rec.Verb != "instance list" || rec.Outcome != evidence.OutcomeOK || len(list) != 2 {
		t.Fatalf("list: %s %v", rec.Outcome, rec.Observations)
	}
	first, _ := list[0].(map[string]any)
	for _, k := range []string{"name", "type", "status", "location"} {
		if _, ok := first[k]; !ok {
			t.Errorf("entry without %s: %v", k, first)
		}
	}
}

func TestInstanceWatch(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", map[string]string{"volatile.uuid": "u1", "volatile.uuid.generation": "g1", "volatile.cloud-init.instance-id": "i1"})
	opts := options(t, fake)
	cmd := &InstanceWatch{ServerCommand: opts, For: 1500 * time.Millisecond, Interval: 100 * time.Millisecond, stdout: &bytes.Buffer{}}
	cmd.Args.Name = "c1"
	done := make(chan error, 1)
	go func() { done <- cmd.Execute(nil) }()
	time.Sleep(300 * time.Millisecond)
	fake.SetInstanceConfig("default", "c1", "volatile.uuid.generation", "g2") // a snapshot restore
	time.Sleep(300 * time.Millisecond)
	fake.SetStatus("default", "c1", "Stopped")
	time.Sleep(300 * time.Millisecond)
	fake.RemoveInstance("default", "c1")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rec := record(t, opts, 1)
	if rec.Verb != "instance watch" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s", rec.Verb, rec.Outcome)
	}
	initial, _ := observation(t, rec, "watch.initial").(map[string]any)
	if initial["uuid"] != "u1" || initial["present"] != true || initial["etag"] == "" {
		t.Errorf("initial %v", initial)
	}
	var changes []struct{ At, Field, From, To string }
	data, _ := json.Marshal(observation(t, rec, "watch.changes"))
	_ = json.Unmarshal(data, &changes)
	var fields []string
	for _, c := range changes {
		fields = append(fields, c.Field)
		if _, err := time.Parse(time.RFC3339Nano, c.At); err != nil {
			t.Errorf("time %q: %v", c.At, err)
		}
	}
	got := strings.Join(fields, ",")
	for _, want := range []string{"uuid.generation", "status", "present"} {
		if !strings.Contains(got, want) {
			t.Errorf("changes %s; want %s among them", got, want)
		}
	}
	if n, _ := observation(t, rec, "watch.polls").(float64); n < 8 {
		t.Errorf("%v polls", n)
	}
	// polls are not artifacts: one per change would be enough to drown a run
	if len(rec.Raw) > 3 {
		t.Errorf("%d artifacts", len(rec.Raw))
	}
}

//go:build lab

package acceptance

import (
	"strings"
	"testing"
	"time"
)

// These test ST-8's judgement without a lab.

func TestParseDebug(t *testing.T) {
	d, ok := parseDebug("pid=4242\nrss= 51200\ngoroutine profile: total 37\n# HeapInuse = 8388608\n")
	if !ok || d.PID != 4242 || d.RSSKiB != 51200 || d.Goroutines != 37 || d.HeapInuse != 8<<20 {
		t.Errorf("%+v %v", d, ok)
	}
	if _, ok := parseDebug("pid=\nrss=\n"); ok {
		t.Error("a sample without a plugin")
	}
}

func TestCountBoth(t *testing.T) {
	inFlight := map[string]bool{"a-001/user.spire.challenge.x": true, "a-002/user.spire.challenge.y": true}
	later := map[string]bool{"a-002/user.spire.challenge.y": true, "a-003/user.spire.challenge.z": true}
	if n := countBoth(inFlight, later); n != 1 {
		t.Errorf("countBoth = %d, want 1: only a-002's key stayed", n)
	}
}

func TestModeAt(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 22, 0, 0, 0, time.UTC)
	events := []soakEvent{
		{Fault: "stop", Start: t0.Add(5 * time.Minute)},
		{Fault: "mode-switch", Start: t0.Add(10 * time.Minute), Mode: "config_push"},
		{Fault: "mode-switch", Start: t0.Add(20 * time.Minute), Mode: "file_pull"},
	}
	for at, want := range map[time.Duration]string{2 * time.Minute: "file_pull", 15 * time.Minute: "config_push", 25 * time.Minute: "file_pull"} {
		if m, sure := modeAt(events, t0.Add(at)); !sure || m != want {
			t.Errorf("%v: %s %v, want %s", at, m, sure, want)
		}
	}
	if _, sure := modeAt(events, t0.Add(10*time.Minute+30*time.Second)); sure {
		t.Error("sure within a minute of a switch")
	}
}

func TestLeaks(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 22, 0, 0, 0, time.UTC)
	var steady, growing, short []debugSample
	for i := range 19 { // 90 minutes, every 5
		at := t0.Add(time.Duration(i) * 5 * time.Minute)
		steady = append(steady, debugSample{At: at, PID: 1, Goroutines: 40 + i%3, HeapInuse: 8 << 20})
		growing = append(growing, debugSample{At: at, PID: 2, Goroutines: 40 + 10*i, HeapInuse: int64(8+4*i) << 20})
		if i < 10 {
			short = append(short, debugSample{At: at, PID: 3, Goroutines: 40 + 100*i})
		}
	}
	if got := leaks(steady); len(got) != 0 {
		t.Errorf("steady: %v", got)
	}
	if got := leaks(growing); len(got) != 2 || !strings.Contains(got[0], "goroutines") || !strings.Contains(got[1], "heap") {
		t.Errorf("growing: %v", got)
	}
	if got := leaks(short); len(got) != 0 {
		t.Errorf("a process shorter than 60 minutes is not judged: %v", got)
	}
}

func TestIsolation(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 22, 0, 0, 0, time.UTC)
	x := &soakRun{s: soakSettings{server: "lxd5"}, uuids: map[string]string{"a-001": "ua"},
		events: []soakEvent{
			{Fault: "hang", Start: t0, End: t0.Add(40 * time.Second)},
			{Fault: "partition", Start: t0.Add(time.Hour), End: t0.Add(time.Hour + 30*time.Second)},
			{Fault: "daemon", Start: t0.Add(2 * time.Hour), End: t0.Add(2*time.Hour + 10*time.Second)}, // too short to judge
			{Fault: "stop", Start: t0.Add(3 * time.Hour), End: t0.Add(3*time.Hour + time.Minute)},      // not an isolation fault
		}}
	y := &soakRun{s: soakSettings{server: "lxd6"}, uuids: map[string]string{"b-001": "ub"}}
	atts := []soakAttest{
		{Time: t0.Add(20 * time.Second), UUID: "ub", OK: true, DurationMS: 30000},
		{Time: t0.Add(time.Hour + 10*time.Second), UUID: "ua", OK: true}, // the faulted server's own
	}
	v, during := isolation([]*soakRun{x, y}, atts)
	if len(v) != 1 || !strings.Contains(v[0], "partition on lxd5") {
		t.Errorf("violations %v", v)
	}
	if len(during["lxd6"]) != 1 || during["lxd6"][0] != 30000 {
		t.Errorf("during %v", during)
	}
}

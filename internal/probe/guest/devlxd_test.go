package guest

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const secret = "#cloud-config\npassword: hunter2\n"

func guestOptions(t *testing.T, sock string) probe.GuestCommand {
	t.Helper()
	return probe.GuestCommand{GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir(), RunID: "r1", JSON: true}, Socket: sock}
}

// records parses the records printed with --json, one after the other.
func records(t *testing.T, out *bytes.Buffer) []evidence.Record {
	t.Helper()
	var recs []evidence.Record
	dec := json.NewDecoder(out)
	for dec.More() {
		var rec evidence.Record
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("printed records: %v", err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func get(t *testing.T, opts probe.GuestCommand, path string) (evidence.Record, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &DevlxdGet{GuestCommand: opts, stdout: out}
	cmd.Args.Path = path
	err := cmd.Execute(nil)
	recs := records(t, out)
	if len(recs) != 1 {
		t.Fatalf("get %s: %d records (%v)", path, len(recs), err)
	}
	return recs[0], err
}

// notIn fails when text appears in any file under the directories, but
// aliases.json: the alias map holds the originals by design (PRE-09).
func notIn(t *testing.T, text string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
			if err == nil && !d.IsDir() && d.Name() != "aliases.json" {
				if data, _ := os.ReadFile(path); strings.Contains(string(data), text) {
					t.Errorf("%s holds %q", path, text)
				}
			}
			return nil
		})
	}
}

func TestDevlxdGet(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	rec, err := get(t, guestOptions(t, fake.Socket), "/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "guest/0001" || rec.Verb != "devlxd get" || !slices.Equal(rec.Args, []string{"/1.0"}) ||
		rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Expectations, []string{"devlxd-server"}) {
		t.Errorf("record %s %q %v %s %v", rec.ID, rec.Verb, rec.Args, rec.Outcome, rec.Expectations)
	}
	for key, want := range map[string]string{
		"devlxd.http_status": `200`,
		"devlxd.format":      `"plain"`,
		"devlxd.content":     `{"api_version":"1.0","auth":"untrusted","instance_type":"container","location":"lxd6","state":"Started","supported_storage_drivers":[]}`,
	} {
		if got := jsonOf(value(t, rec, key)); got != want {
			t.Errorf("%s = %s; want %s", key, got, want)
		}
	}
	value(t, rec, "devlxd.latency_ms")

	// the same in an envelope (as over vsock)
	fake = fakelxd.NewDevLXD(t)
	fake.Envelope = true
	rec, _ = get(t, guestOptions(t, fake.Socket), "/1.0")
	if rec.Outcome != evidence.OutcomeOK || jsonOf(value(t, rec, "devlxd.format")) != `"envelope"` ||
		!strings.Contains(jsonOf(value(t, rec, "devlxd.content")), `"instance_type":"container"`) {
		t.Errorf("envelope: %s %v", rec.Outcome, rec.Observations)
	}
}

func TestDevlxdGetConfigKey(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.Config["cloud-init.user-data"] = secret
	fake.Config["user.lxd-probe.a"] = "visible"
	fake.Config["volatile.uuid"] = "hidden-from-guests"
	opts := guestOptions(t, fake.Socket)
	opts.Record = t.TempDir()

	// a redacted key's bare value never reaches evidence or fixtures
	rec, err := get(t, opts, "/1.0/config/cloud-init.user-data")
	if err != nil || rec.Outcome != evidence.OutcomeOK || jsonOf(value(t, rec, "devlxd.content")) != `"[redacted]"` {
		t.Errorf("cloud-init.user-data: %v %s %v", err, rec.Outcome, rec.Observations)
	}
	notIn(t, "hunter2", opts.EvidenceDir, opts.Record)
	if files, _ := filepath.Glob(filepath.Join(opts.Record, "devlxd", "0001-01-get-1.0-config-cloud-init.user-data.*.json")); len(files) != 2 {
		t.Errorf("fixtures: %v", files)
	}

	rec, _ = get(t, opts, "/1.0/config/user.lxd-probe.a")
	if jsonOf(value(t, rec, "devlxd.content")) != `"visible"` {
		t.Errorf("user.lxd-probe.a: %v", rec.Observations)
	}

	// a key the guest may not read: a finding, not an error
	rec, err = get(t, opts, "/1.0/config/volatile.uuid")
	if err != nil || rec.Outcome != evidence.OutcomeUnexpected || jsonOf(value(t, rec, "devlxd.http_status")) != `403` ||
		jsonOf(value(t, rec, "devlxd.error")) != `"Forbidden"` {
		t.Errorf("volatile.uuid: %v %s %v", err, rec.Outcome, rec.Observations)
	}
}

func TestDevlxdGetEvents(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	rec, err := get(t, guestOptions(t, fake.Socket), "/1.0/events")
	// a stream that sends nothing within the deadline is recorded as such
	if err != nil || rec.Outcome != evidence.OutcomeOK || !strings.Contains(jsonOf(value(t, rec, "devlxd.events")), "no response within") {
		t.Errorf("events: %v %s %v", err, rec.Outcome, rec.Observations)
	}
}

func TestDevlxdGetNoSocket(t *testing.T) {
	rec, err := get(t, guestOptions(t, filepath.Join(t.TempDir(), "absent")), "/1.0")
	if exit.Code(err) != exit.Connection || rec.Outcome != evidence.OutcomeError {
		t.Errorf("no socket: exit %d (%v), outcome %s", exit.Code(err), err, rec.Outcome)
	}
}

func TestDevlxdDump(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.Config["cloud-init.user-data"] = secret
	fake.Config["user.lxd-probe.a"] = "visible"
	fake.Config["volatile.uuid"] = "not listed: the guest may not read it"
	opts := guestOptions(t, fake.Socket)
	opts.Record = t.TempDir()
	out := &bytes.Buffer{}
	cmd := &DevlxdDump{GuestCommand: opts, stdout: out}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	recs := records(t, out)
	var paths []string
	for i, rec := range recs {
		if rec.Verb != "devlxd dump" || len(rec.Args) != 1 || rec.ID != "guest/"+[]string{"0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008"}[i] {
			t.Errorf("record %d: %s %q %v", i, rec.ID, rec.Verb, rec.Args)
			continue
		}
		paths = append(paths, rec.Args[0])
		if rec.Outcome != evidence.OutcomeOK {
			t.Errorf("%s: %s %v", rec.Args[0], rec.Outcome, rec.Observations)
		}
	}
	want := []string{"/", "/1.0", "/1.0/config", "/1.0/config/cloud-init.user-data", "/1.0/config/user.lxd-probe.a", "/1.0/meta-data", "/1.0/devices", "/1.0/events"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths %v; want %v", paths, want)
	}
	notIn(t, "hunter2", opts.EvidenceDir, opts.Record)
	notIn(t, "00:16:3e:0e:4b:fd", opts.EvidenceDir, opts.Record) // a MAC address, aliased
}

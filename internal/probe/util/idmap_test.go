package util

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
)

const cRaw = `[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000},` +
	`{"Isuid":true,"Isgid":false,"Hostid":1000,"Nsid":1000,"Maprange":1},` +
	`{"Isuid":true,"Isgid":false,"Hostid":1001001,"Nsid":1001,"Maprange":999998999}]`

// writeRecord writes a record with the given observations and returns its
// path.
func writeRecord(t *testing.T, dir, name, id string, obs map[string]any) string {
	t.Helper()
	rec := evidence.Record{ID: id, Verb: name}
	for k, v := range obs {
		rec.Observations = append(rec.Observations, evidence.Observation{Key: k, Value: v})
	}
	data, _ := json.Marshal(rec)
	f, err := os.CreateTemp(dir, name+"-*.json") // unique: tables build every record first
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close() //nolint:errcheck
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func uidMap(entries ...[3]int) []map[string]int {
	var out []map[string]int
	for _, e := range entries {
		out = append(out, map[string]int{"inside": e[0], "outside": e[1], "count": e[2]})
	}
	return out
}

func utilOpts(t *testing.T) probe.GlobalOptions {
	return probe.GlobalOptions{EvidenceDir: t.TempDir(), RunID: "r1", JSON: true}
}

func lastRecord(t *testing.T, out *bytes.Buffer) evidence.Record {
	t.Helper()
	var rec evidence.Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("printed record %q: %v", out.String(), err)
	}
	return rec
}

func obs(t *testing.T, rec evidence.Record, key string) any {
	t.Helper()
	for _, o := range rec.Observations {
		if o.Key == key {
			return o.Value
		}
	}
	t.Fatalf("no observation %q in %v", key, rec.Observations)
	return nil
}

func TestIdmapCheck(t *testing.T) {
	dir := t.TempDir()
	instance := func(typ, current string, withMap bool) string {
		cfg := map[string]string{"volatile.idmap.next": "[]"}
		if withMap {
			cfg["volatile.idmap.current"] = current
		}
		return writeRecord(t, dir, "instance-"+typ+current[:2], "server/0002", map[string]any{"instance.type": typ, "instance.config": cfg})
	}
	preflight := func(m []map[string]int) string {
		return writeRecord(t, dir, "preflight", "guest/0001", map[string]any{"idmap.uid_map": m})
	}
	for name, tt := range map[string]struct {
		inst, pre string
		want      string
		outcome   evidence.Outcome
	}{
		"match":      {instance("container", cRaw, true), preflight(uidMap([3]int{0, 1000000, 1000}, [3]int{1000, 1000, 1}, [3]int{1001, 1001001, 999998999})), "match", evidence.OutcomeOK},
		"mismatch":   {instance("container", cRaw, true), preflight(uidMap([3]int{0, 1065536, 65536})), "mismatch", evidence.OutcomeUnexpected},
		"privileged": {instance("container", "[]", true), preflight(uidMap([3]int{0, 0, 4294967295})), "not-applicable", evidence.OutcomeOK},
		"vm":         {instance("virtual-machine", "--", false), preflight(uidMap([3]int{0, 0, 4294967295})), "not-applicable", evidence.OutcomeOK},
	} {
		out := &bytes.Buffer{}
		cmd := &IdmapCheck{GlobalOptions: utilOpts(t), stdout: out}
		cmd.Args.Instance, cmd.Args.Preflight = tt.inst, tt.pre
		if err := cmd.Execute(nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rec := lastRecord(t, out)
		if got := obs(t, rec, "idmap.check"); got != tt.want || rec.Outcome != tt.outcome {
			t.Errorf("%s: %v, %s; want %s, %s (%v)", name, got, rec.Outcome, tt.want, tt.outcome, rec.Observations)
		}
		// file names only: a full path may hold a user's name
		for _, a := range rec.Args {
			if strings.Contains(a, "/") {
				t.Errorf("%s: argument %q is a path", name, a)
			}
		}
	}
}

func TestIdmapTranslate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "current.json")
	if err := os.WriteFile(file, []byte(cRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		idmap, uidMap string
		uid           uint32
		key           string
		want          string
	}{
		"literal":         {cRaw, "", 1000, "idmap.host_uid", "1000"},
		"file":            {file, "", 0, "idmap.host_uid", "1000000"},
		"refused":         {`[{"Isuid":true,"Hostid":1,"Nsid":0,"Maprange":5}]`, "", 99, "idmap.refusal", `{"detail":"0 entries map uid 99","reason":"no-entry"}`},
		"uid_map agrees":  {cRaw, "0 1000000 1000\n1000 1000 1\n1001 1001001 999998999\n", 1001, "idmap.uid_map_agrees", "true"},
		"uid_map differs": {cRaw, "0 1065536 65536\n", 0, "idmap.uid_map_agrees", "false"},
	} {
		out := &bytes.Buffer{}
		cmd := &IdmapTranslate{GlobalOptions: utilOpts(t), VolatileIdmap: tt.idmap, UIDMap: tt.uidMap, UID: tt.uid, stdout: out}
		if err := cmd.Execute(nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rec := lastRecord(t, out)
		data, _ := json.Marshal(obs(t, rec, tt.key))
		if string(data) != tt.want {
			t.Errorf("%s: %s = %s; want %s", name, tt.key, data, tt.want)
		}
	}
}

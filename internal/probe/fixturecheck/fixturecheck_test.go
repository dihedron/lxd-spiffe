package fixturecheck

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

func fixture(method, path, query string, status int, header http.Header, body any) evidence.Fixture {
	data, _ := json.Marshal(body)
	return evidence.Fixture{
		Name:     "f",
		Request:  evidence.FixtureRequest{Method: method, Path: path, Query: query},
		Response: evidence.FixtureResponse{Status: status, Header: header, Body: data},
	}
}

func sync(metadata any) map[string]any {
	return map[string]any{"type": "sync", "status": "Success", "status_code": 200, "metadata": metadata}
}

func TestCheck(t *testing.T) {
	trusted := map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "trusted",
		"environment": map[string]any{"server_version": "6.9", "server_clustered": false}}
	untrusted := map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "untrusted", "public": false}
	noEnv := map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "trusted"}
	extra := map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "trusted", "brand_new": 1,
		"environment": map[string]any{"server_version": "6.9", "server_clustered": false}}
	etag := http.Header{"Etag": {`"x"`}}
	for _, tc := range []struct {
		name    string
		f       evidence.Fixture
		kind    Kind
		failed  bool
		missing []string
		unknown []string
	}{
		{"trusted GET /1.0", fixture("GET", "/1.0", "", 200, nil, sync(trusted)), Decoded, false, nil, nil},
		{"untrusted GET /1.0 has no environment", fixture("GET", "/1.0", "", 200, nil, sync(untrusted)), Decoded, false, nil, nil},
		{"trusted GET /1.0 without environment", fixture("GET", "/1.0", "", 200, nil, sync(noEnv)), Decoded, true,
			[]string{"environment.server_version", "environment.server_clustered"}, nil},
		{"a field the SDK does not know", fixture("GET", "/1.0", "", 200, nil, sync(extra)), Decoded, false, nil, []string{"brand_new"}},
		{"a decode error", fixture("GET", "/1.0", "", 200, nil, sync(map[string]any{"api_extensions": "not a list"})), Decoded, true, nil, nil},
		{"instance without ETag", fixture("GET", "/1.0/instances/c1", "project=default", 200, nil, sync(map[string]any{"name": "c1"})), Decoded, true, nil, nil},
		{"files API headers", fixture("GET", "/1.0/instances/c1/files", "path=%2Fetc", 200,
			http.Header{"X-Lxd-Uid": {"0"}, "X-Lxd-Gid": {"0"}, "X-Lxd-Mode": {"0644"}, "X-Lxd-Type": {"file"}}, "text"), Checked, true,
			[]string{"header X-LXD-modified"}, nil},
		{"PATCH answers sync", fixture("PATCH", "/1.0/instances/c1", "", 200, etag, sync(map[string]any{})), Checked, false, nil, nil},
		{"PATCH answering async", fixture("PATCH", "/1.0/instances/c1", "", 200, etag,
			map[string]any{"type": "async", "metadata": map[string]any{}}), Checked, true, []string{"envelope type sync (got async)"}, nil},
		{"error envelope", fixture("GET", "/1.0/instances/x", "", 404, nil, map[string]any{"type": "error", "error": "Not Found", "error_code": 404}), Error, false, nil, nil},
		{"error without envelope", fixture("GET", "/1.0/instances/x", "", 403, nil, "forbidden"), Error, true, nil, nil},
		{"project with config", fixture("GET", "/1.0/projects/l16-on", "", 200, etag,
			sync(map[string]any{"name": "l16-on", "config": map[string]string{"features.networks": "true"}})), Decoded, false, nil, nil},
		{"project without config", fixture("GET", "/1.0/projects/l16-on", "", 200, etag, sync(map[string]any{"name": "l16-on"})), Decoded, true,
			[]string{"config"}, nil},
		{"a call the probe does not make", fixture("DELETE", "/1.0/instances/c1", "", 200, nil, sync(nil)), Skipped, false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Check(tc.f)
			if r.Kind != tc.kind || r.Failed() != tc.failed {
				t.Errorf("kind %s failed %v; want %s %v (%+v)", r.Kind, r.Failed(), tc.kind, tc.failed, r)
			}
			if tc.missing != nil && !slices.Equal(r.Missing, tc.missing) {
				t.Errorf("missing %v; want %v", r.Missing, tc.missing)
			}
			if tc.unknown != nil && !slices.Equal(r.Unknown, tc.unknown) {
				t.Errorf("unknown %v; want %v", r.Unknown, tc.unknown)
			}
		})
	}
}

func TestCheckDevLXD(t *testing.T) {
	plain := fixture("GET", "/1.0", "", 200, nil, map[string]any{"api_version": "1.0", "location": "none"})
	enveloped := fixture("GET", "/1.0", "", 200, nil, sync(map[string]any{"api_version": "1.0", "location": "none"}))
	for name, f := range map[string]evidence.Fixture{"plain": plain, "envelope": enveloped} {
		if r := CheckDevLXD(f); r.Kind != Decoded || r.Failed() {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := CheckDevLXD(fixture("GET", "/1.0/meta-data", "", 200, nil, "text")); r.Kind != Skipped {
		t.Errorf("meta-data: %+v", r)
	}
}

// TestVerify loads a directory laid out as test/fixtures: the server's
// fixtures, and a devlxd/<instance>/ directory below.
func TestVerify(t *testing.T) {
	dir := t.TempDir()
	write := func(sub, stem string, f evidence.Fixture) {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		req, _ := json.Marshal(map[string]any{"method": f.Request.Method, "path": f.Request.Path, "query": f.Request.Query})
		resp, _ := json.Marshal(f.Response)
		for name, data := range map[string][]byte{stem + ".request.json": req, stem + ".response.json": resp} {
			if err := os.WriteFile(filepath.Join(d, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("", "0001-01-get-1.0", fixture("GET", "/1.0", "", 200, nil, sync(map[string]any{"api_extensions": []string{}, "api_version": "1.0", "auth": "untrusted"})))
	write("devlxd/c1", "0001-01-get-1.0", fixture("GET", "/1.0", "", 200, nil, map[string]any{"api_version": "1.0"}))
	results, err := Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range results {
		names = append(names, r.Fixture+" "+r.Call)
	}
	want := []string{"0001-01-get-1.0 GET /1.0", "devlxd/c1/0001-01-get-1.0 devLXD GET /1.0"}
	if !slices.Equal(names, want) {
		t.Errorf("results %v; want %v", names, want)
	}
	if s := Summarize(results); s.Total != 2 || len(s.Failed) != 0 || s.Kinds[Decoded] != 2 {
		t.Errorf("summary %+v", s)
	}
}

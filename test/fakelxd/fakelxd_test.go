package fakelxd_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl"

// calls is a small session: a server read, an instance read, a write with
// the right ETag, the same write with a stale one, a file read and a miss.
func calls(t *testing.T, c *lxd.Client) []*lxd.RawResponse {
	t.Helper()
	ctx := context.Background()
	var out []*lxd.RawResponse
	do := func(r lxd.RawRequest) *lxd.RawResponse {
		resp, err := c.Raw(ctx, r)
		if err != nil {
			t.Fatalf("%s %s: %v", r.Method, r.Path, err)
		}
		out = append(out, resp)
		return resp
	}
	do(lxd.RawRequest{Method: http.MethodGet, Path: "/1.0"})
	inst := do(lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/instances/c1"})
	etag := inst.Header.Get("ETag")
	do(lxd.RawRequest{Method: http.MethodPatch, Path: "/1.0/instances/c1", Body: []byte(`{"config":{"user.lxd-probe.a":"2"}}`), ETag: etag})
	do(lxd.RawRequest{Method: http.MethodPatch, Path: "/1.0/instances/c1", Body: []byte(`{"config":{"user.lxd-probe.a":"3"}}`), ETag: etag})
	do(lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/instances/c1/files", Query: map[string][]string{"path": {"/run/lxd-probe/proof"}}})
	do(lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/instances/missing"})
	return out
}

func TestRecordThenReplay(t *testing.T) {
	assumed := fakelxd.NewAssumed(t)
	assumed.AddInstance("default", "c1", map[string]string{"user.lxd-probe.a": "1", "user.secret": "private"})
	assumed.AddFile("default", "c1", "/run/lxd-probe/proof", fakelxd.File{Content: "abcd", UID: 0, GID: 0, Mode: 0o600, Type: "file"})

	dir := t.TempDir()
	writer := &evidence.FixtureWriter{Dir: dir, Sanitizer: evidence.NewSanitizer(evidence.NewAliases())}
	seq := 0
	recorder := func(e lxd.Exchange) {
		seq++
		if err := writer.Write("6.5", 1, seq, e.Method, e.Path, e.Query, e.RequestHeader, e.RequestBody, e.Status, e.ResponseHeader, e.ResponseBody); err != nil {
			t.Errorf("recording: %v", err)
		}
	}
	c, err := lxd.Connect(context.Background(), lxd.Config{Endpoint: assumed.URL, Pin: assumed.Fingerprint(), Project: "default", BearerToken: token, Recorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	recorded := calls(t, c)
	if recorded[3].Status != http.StatusPreconditionFailed || recorded[5].Status != http.StatusNotFound {
		t.Fatalf("statuses = %d, %d; want 412, 404", recorded[3].Status, recorded[5].Status)
	}

	// the fixtures are sanitized
	files, _ := filepath.Glob(filepath.Join(dir, "6.5", "*.json"))
	if len(files) != 12 {
		t.Fatalf("fixture files = %d; want 12", len(files))
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, leak := range []string{"Authorization", token, "private"} {
			if bytes.Contains(data, []byte(leak)) {
				t.Errorf("%s contains %q", filepath.Base(f), leak)
			}
		}
	}
	if !strings.HasSuffix(files[0], "0001-01-get-1.0.request.json") {
		t.Errorf("first fixture = %s", filepath.Base(files[0]))
	}

	// replaying gives the same answers
	replay := fakelxd.NewReplay(t, dir)
	r, err := lxd.Connect(context.Background(), lxd.Config{Endpoint: replay.URL, Pin: replay.Fingerprint(), Project: "default"})
	if err != nil {
		t.Fatal(err)
	}
	replayed := calls(t, r)
	for i := range recorded {
		if recorded[i].Status != replayed[i].Status {
			t.Errorf("call %d: status %d, replayed %d", i, recorded[i].Status, replayed[i].Status)
		}
		if !jsonEqual(recorded[i].Body, replayed[i].Body) {
			t.Errorf("call %d: body\n%s\nreplayed\n%s", i, recorded[i].Body, replayed[i].Body)
		}
	}
	if replayed[1].Header.Get("ETag") != recorded[1].Header.Get("ETag") {
		t.Errorf("ETag not replayed")
	}
	if replayed[4].Header.Get("X-LXD-uid") != "0" || string(replayed[4].Body) != "abcd" {
		t.Errorf("file not replayed: %v %q", replayed[4].Header, replayed[4].Body)
	}
}

// jsonEqual compares two bodies after sanitizing both: the recorded body
// still holds the values that the fixture redacted (user.secret).
func jsonEqual(a, b []byte) bool {
	ea, errA := evidence.NewSanitizer(evidence.NewAliases()).JSON(a)
	eb, errB := evidence.NewSanitizer(evidence.NewAliases()).JSON(b)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ea, eb)
}

// TestHideProject checks that a hidden project answers like LXD does to an
// identity that may not view it: 404 Not Found, also for its own entities.
func TestHideProject(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("restricted", "c1", nil)
	fake.AddInstance("default", "c1", nil)
	fake.HideProject("restricted")
	c, err := lxd.Connect(context.Background(), lxd.Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), Project: "restricted", BearerToken: token})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/1.0/instances/c1", "/1.0/instances/missing"} {
		resp, err := c.Raw(context.Background(), lxd.RawRequest{Method: http.MethodGet, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != http.StatusNotFound || !strings.Contains(string(resp.Body), `"error":"Not Found"`) {
			t.Errorf("%s in a hidden project: %d %s", path, resp.Status, resp.Body)
		}
	}
	c, err = lxd.Connect(context.Background(), lxd.Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), Project: "default", BearerToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := c.Raw(context.Background(), lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/instances/c1"}); err != nil || resp.Status != http.StatusOK {
		t.Errorf("default/c1: %v %v", resp, err)
	}
}

// TestDevLXDRecordThenReplay records devLXD exchanges as fixtures and
// replays them on a Unix socket.
func TestDevLXDRecordThenReplay(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.Config["user.lxd-probe.a"] = "1" // not redacted: the lab's own key
	dir := t.TempDir()
	writer := &evidence.FixtureWriter{Dir: dir, Sanitizer: evidence.NewSanitizer(evidence.NewAliases())}
	seq := 0
	rec, err := devlxd.Connect(context.Background(), devlxd.Config{Socket: fake.Socket, Recorder: func(x lxd.Exchange) {
		seq++
		if err := writer.Write("devlxd", 1, seq, x.Method, x.Path, x.Query, x.RequestHeader, x.RequestBody, x.Status, x.ResponseHeader, x.ResponseBody); err != nil {
			t.Errorf("recording: %v", err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/1.0", "/1.0/config", "/1.0/config/user.lxd-probe.a", "/1.0/config/volatile.x"}
	var recorded []*devlxd.Response
	for _, p := range paths {
		resp, err := rec.Get(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		recorded = append(recorded, resp)
	}

	replay, err := devlxd.Connect(context.Background(), devlxd.Config{Socket: fakelxd.NewDevLXDReplay(t, filepath.Join(dir, "devlxd"))})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		resp, err := replay.Get(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		// JSON bodies are stored as JSON, re-indented
		same := jsonEqual(resp.Body, recorded[i].Body) || bytes.Equal(bytes.TrimSpace(resp.Body), bytes.TrimSpace(recorded[i].Body))
		if resp.Status != recorded[i].Status || !same {
			t.Errorf("%s: replayed %d %q; recorded %d %q", p, resp.Status, resp.Body, recorded[i].Status, recorded[i].Body)
		}
	}
}

package lxd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// fixtureVersions are the LXD versions whose recorded fixtures the
// operations are verified against (FR-S17 (11), gate L15).
var fixtureVersions = []string{"5.21.8", "6.9"}

func fixtureDir(version string) string {
	return filepath.Join("..", "..", "test", "fixtures", version)
}

func connectFake(t *testing.T, s *fakelxd.Server) *Client {
	t.Helper()
	c, err := Connect(context.Background(), Config{Endpoint: s.URL, Pin: s.Fingerprint(), Project: "default"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// wantKind is the error kind a fixture's response calls for.
func wantKind(f evidence.Fixture) Kind {
	if f.Response.Status < 300 {
		return KindNone
	}
	var env api.Response
	if json.Unmarshal(f.Response.ResponseBody(), &env) == nil && f.Response.Status == http.StatusInternalServerError && env.Error == agentNotRunning {
		return KindAgentNotRunning
	}
	return StatusKind(f.Response.Status)
}

func checkKind(t *testing.T, f evidence.Fixture, err error) bool {
	t.Helper()
	if got, want := KindOf(err), wantKind(f); got != want {
		t.Errorf("%s/%s: kind %q, want %q (%v)", f.Path, f.Name, got, want, err)
		return false
	}
	return err == nil
}

// fixtureCall checks one recorded request through the operation that
// sends it; query holds the request's parameters but the project.
type fixtureCall struct {
	// query lists the parameters the operation sends besides the project:
	// a fixture with others was recorded by another request.
	query []string
	check func(t *testing.T, c *Client, f evidence.Fixture, query url.Values, parts []string)
}

// fixtureCalls are the operations by the request path they send, split on
// "/" after /1.0.
var fixtureCalls = map[string]fixtureCall{
	"server": {nil, func(t *testing.T, c *Client, f evidence.Fixture, _ url.Values, _ []string) {
		srv, err := c.Server(context.Background())
		if checkKind(t, f, err) {
			if err := CheckServer(srv, RequiredExtensions...); (err == nil) != (srv.Auth == "trusted") {
				t.Errorf("%s: auth %q, CheckServer %v", f.Path, srv.Auth, err)
			}
		}
	}},
	"identity": {nil, func(t *testing.T, c *Client, f evidence.Fixture, _ url.Values, _ []string) {
		info, err := c.CurrentIdentity(context.Background())
		if checkKind(t, f, err) && info.AuthenticationMethod == "" {
			t.Errorf("%s: no authentication method", f.Path)
		}
	}},
	"project": {nil, func(t *testing.T, c *Client, f evidence.Fixture, _ url.Values, parts []string) {
		p, _, err := c.Project(context.Background(), parts[2])
		if checkKind(t, f, err) && p.Name != parts[2] {
			t.Errorf("%s: project %q", f.Path, p.Name)
		}
	}},
	"uuid": {[]string{"filter", "recursion"}, func(t *testing.T, c *Client, f evidence.Fixture, query url.Values, _ []string) {
		id := strings.TrimPrefix(query.Get("filter"), "config.volatile.uuid eq ")
		names, err := c.InstancesByUUID(context.Background(), c.project, id)
		if !checkKind(t, f, err) {
			return
		}
		var env struct {
			Metadata []json.RawMessage `json:"metadata"`
		}
		if err := json.Unmarshal(f.Response.ResponseBody(), &env); err != nil {
			t.Fatal(err)
		}
		if len(names) != len(env.Metadata) {
			t.Errorf("%s: %d names, fixture lists %d", f.Path, len(names), len(env.Metadata))
		}
	}},
	"instance": {nil, func(t *testing.T, c *Client, f evidence.Fixture, _ url.Values, parts []string) {
		inst, etag, err := c.Instance(context.Background(), c.project, parts[2])
		if !checkKind(t, f, err) {
			return
		}
		if inst.Name != parts[2] || inst.ExpandedConfig["volatile.uuid"] == "" {
			t.Errorf("%s: name %q, uuid %q", f.Path, inst.Name, inst.ExpandedConfig["volatile.uuid"])
		}
		if etag == "" || etag != f.Response.Header.Get("Etag") {
			t.Errorf("%s: ETag %q, fixture %q", f.Path, etag, f.Response.Header.Get("Etag"))
		}
	}},
	"patch": {nil, func(t *testing.T, c *Client, f evidence.Fixture, _ url.Values, parts []string) {
		var body struct {
			Config map[string]*string `json:"config"`
		}
		if err := json.Unmarshal(f.Request.Body, &body); err != nil || len(body.Config) == 0 {
			t.Fatalf("%s: request body %s: %v", f.Path, f.Request.Body, err)
		}
		checkKind(t, f, c.PatchConfig(context.Background(), c.project, parts[2], f.Request.Header.Get("If-Match"), body.Config))
	}},
	"file": {[]string{"path"}, func(t *testing.T, c *Client, f evidence.Fixture, query url.Values, parts []string) {
		file, err := c.File(context.Background(), c.project, parts[2], query.Get("path"))
		if !checkKind(t, f, err) {
			return
		}
		h := f.Response.Header
		mode, _ := strconv.ParseInt(h.Get("X-Lxd-Mode"), 8, 32)
		if strconv.FormatInt(file.UID, 10) != h.Get("X-Lxd-Uid") || strconv.FormatInt(file.GID, 10) != h.Get("X-Lxd-Gid") ||
			int64(file.Mode) != mode || file.Type != h.Get("X-Lxd-Type") {
			t.Errorf("%s: %+v, fixture headers %v", f.Path, file, h)
		}
	}},
}

// callOf names the operation a recorded request belongs to, or "".
func callOf(f evidence.Fixture, query url.Values) (string, []string) {
	parts := strings.Split(strings.TrimPrefix(f.Request.Path, "/1.0"), "/")
	switch {
	case f.Request.Path == "/1.0":
		return "server", parts
	case f.Request.Path == "/1.0/auth/identities/current":
		return "identity", parts
	case len(parts) == 3 && parts[1] == "projects":
		return "project", parts
	case f.Request.Path == "/1.0/instances" && query.Has("filter"):
		return "uuid", parts
	case len(parts) == 3 && parts[1] == "instances":
		return "instance", parts
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "files":
		return "file", parts
	}
	return "", nil
}

// TestFRS17Fixtures replays every recorded request the attestor's
// operations send, on both versions: each answers with the fixture's data
// or with the error kind its status calls for. A fixture recorded by
// another request (other parameters, another method) is left alone, and
// each project has its own connection, so every replay queue keeps its
// order.
func TestFRS17Fixtures(t *testing.T) {
	for _, version := range fixtureVersions {
		t.Run(version, func(t *testing.T) { replayFixtures(t, version) })
	}
}

// replayFixtures replays every fixture of version that an operation sends.
func replayFixtures(t *testing.T, version string) {
	fixtures, err := evidence.LoadFixtures(fixtureDir(version))
	if err != nil {
		t.Fatal(err)
	}
	fake := fakelxd.NewReplay(t, fixtureDir(version))
	clients := map[string]*Client{}
	calls := map[string]int{}
	for _, f := range fixtures {
		name, project, query, parts, ok := operationOf(t, f)
		if !ok {
			continue
		}
		if clients[project] == nil {
			clients[project] = connectReplay(t, fake, project)
		}
		calls[name]++
		fixtureCalls[name].check(t, clients[project], f, query, parts)
	}
	for name := range fixtureCalls {
		if calls[name] == 0 {
			t.Errorf("no fixture exercised %s", name)
		}
	}
	t.Logf("requests replayed: %v", calls)
}

// operationOf returns the operation that sends f's request, its project
// and its other parameters; ok is false for a devLXD fixture, another
// method, or parameters no operation sends.
func operationOf(t *testing.T, f evidence.Fixture) (name, project string, query url.Values, parts []string, ok bool) {
	t.Helper()
	if strings.HasPrefix(f.Path, "devlxd") || f.Request.Method != http.MethodGet && f.Request.Method != http.MethodPatch {
		return "", "", nil, nil, false
	}
	query, err := url.ParseQuery(f.Request.Query)
	if err != nil {
		t.Fatal(err)
	}
	name, parts = callOf(f, query)
	if f.Request.Method == http.MethodPatch {
		name = map[string]string{"instance": "patch"}[name]
	}
	call, ok := fixtureCalls[name]
	project = query.Get("project")
	query.Del("project")
	return name, project, query, parts, ok && sameKeys(query, call.query)
}

func connectReplay(t *testing.T, fake *fakelxd.Server, project string) *Client {
	t.Helper()
	c, err := Connect(context.Background(), Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), Project: project})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sameKeys reports whether query has exactly the parameters keys.
func sameKeys(query url.Values, keys []string) bool {
	if len(query) != len(keys) {
		return false
	}
	for _, k := range keys {
		if !query.Has(k) {
			return false
		}
	}
	return true
}

// TestFRS12CheckServer: a server must be trusted and offer every required
// extension; the error names the missing ones.
func TestFRS12CheckServer(t *testing.T) {
	srv := &api.Server{ServerUntrusted: api.ServerUntrusted{Auth: "trusted", APIExtensions: []string{"projects", "instance_generation_id", "api_filtering"}}}
	if err := CheckServer(srv, RequiredExtensions...); err != nil {
		t.Errorf("trusted, all extensions: %v", err)
	}
	if err := CheckServer(srv, append(RequiredExtensions, "auth_bearer")...); err == nil || !strings.Contains(err.Error(), "auth_bearer") {
		t.Errorf("missing auth_bearer: %v", err)
	}
	srv.Auth = "untrusted"
	if err := CheckServer(srv, RequiredExtensions...); err == nil {
		t.Error("untrusted server accepted")
	}
}

// TestFRS17FileHeaders: the files API's headers are required and strict,
// every mode bit kept: the proof check relies on owner, mode and type.
func TestFRS17FileHeaders(t *testing.T) {
	good := http.Header{"X-Lxd-Uid": {"0"}, "X-Lxd-Gid": {"0"}, "X-Lxd-Mode": {"0600"}, "X-Lxd-Type": {"file"}}
	f, err := parseFileHeaders(good)
	if err != nil || f.UID != 0 || f.GID != 0 || f.Mode != 0o600 || f.Type != "file" {
		t.Fatalf("good headers: %+v %v", f, err)
	}
	setuid := good.Clone()
	setuid.Set("X-Lxd-Mode", "4600")
	if f, err := parseFileHeaders(setuid); err != nil || f.Mode != 0o4600 {
		t.Errorf("setuid bit lost: %+v %v", f, err)
	}
	for name, edit := range map[string]func(http.Header){
		"no type":       func(h http.Header) { h.Del("X-Lxd-Type") },
		"unknown type":  func(h http.Header) { h.Set("X-Lxd-Type", "fifo") },
		"no uid":        func(h http.Header) { h.Del("X-Lxd-Uid") },
		"negative uid":  func(h http.Header) { h.Set("X-Lxd-Uid", "-1") },
		"no gid":        func(h http.Header) { h.Del("X-Lxd-Gid") },
		"short mode":    func(h http.Header) { h.Set("X-Lxd-Mode", "600") },
		"decimal mode":  func(h http.Header) { h.Set("X-Lxd-Mode", "0680") },
		"no mode":       func(h http.Header) { h.Del("X-Lxd-Mode") },
		"huge uid":      func(h http.Header) { h.Set("X-Lxd-Uid", "99999999999") },
		"uid with sign": func(h http.Header) { h.Set("X-Lxd-Uid", "+0") },
	} {
		h := good.Clone()
		edit(h)
		if _, err := parseFileHeaders(h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestFRS18InstancesByUUIDRefusesMalformed: the UUID goes into a filter
// expression, so anything but a canonical UUID is refused before any
// request (the fake fails the test on an unexpected request).
func TestFRS18InstancesByUUIDRefusesMalformed(t *testing.T) {
	c := connectFake(t, fakelxd.NewReplay(t, t.TempDir()))
	for _, id := range []string{"", "x", "FBF73246-B07C-4291-AFD9-12AE2BE70828", "fbf73246-b07c-4291-afd9-12ae2be70828 or 1", "urn:uuid:fbf73246-b07c-4291-afd9-12ae2be70828"} {
		if _, err := c.InstancesByUUID(context.Background(), "default", id); err == nil {
			t.Errorf("%q: accepted", id)
		}
	}
}

// TestFRS17FilesLimit: a files API response over the 4 KiB limit is an
// error of kind too-large, not a truncated proof (SEC-11).
func TestFRS17FilesLimit(t *testing.T) {
	s := fakelxd.NewAssumed(t)
	s.AddInstance("default", "c1", nil)
	s.AddFile("default", "c1", "/big", fakelxd.File{Content: strings.Repeat("x", limits.MaxFilesResponse+1), Mode: 0o600, Type: "file"})
	_, err := connectFake(t, s).File(context.Background(), "default", "c1", "/big")
	if KindOf(err) != KindTooLarge {
		t.Errorf("kind %q (%v)", KindOf(err), err)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Errorf("not an *Error: %v", err)
	}
}

// TestFRS17InstanceValidated: an instance read whose name, project or type
// is not the one asked for is a protocol error, not data to decide on.
func TestFRS17InstanceValidated(t *testing.T) {
	for name, meta := range map[string]string{
		"other name":    `{"name": "other", "project": "default", "type": "container", "status": "Running"}`,
		"other project": `{"name": "c1", "project": "elsewhere", "type": "container", "status": "Running"}`,
		"unknown type":  `{"name": "c1", "project": "default", "type": "zone", "status": "Running"}`,
	} {
		dir := t.TempDir()
		writeFixture(t, dir, "0001-01-get-1.0-instances-c1", `{"method": "GET", "path": "/1.0/instances/c1", "query": "project=default"}`,
			`{"status": 200, "header": {"Etag": ["\"x\""]}, "body": {"type": "sync", "status": "Success", "status_code": 200, "metadata": `+meta+`}}`)
		_, _, err := connectFake(t, fakelxd.NewReplay(t, dir)).Instance(context.Background(), "default", "c1")
		if KindOf(err) != KindProtocol {
			t.Errorf("%s: kind %q (%v)", name, KindOf(err), err)
		}
	}
}

func writeFixture(t *testing.T, dir, stem, request, response string) {
	t.Helper()
	for suffix, body := range map[string]string{".request.json": request, ".response.json": response} {
		if err := os.WriteFile(filepath.Join(dir, stem+suffix), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFRS13PatchConfig: a PATCH sets and removes keys under If-Match, a
// stale ETag is KindPrecondition, and an operation answer is waited for.
func TestFRS13PatchConfig(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("default", "c1", map[string]string{"user.a": "1"})
	c := connectFake(t, fake)
	ctx := context.Background()
	_, etag, err := c.Instance(ctx, "default", "c1")
	if err != nil {
		t.Fatal(err)
	}
	value := "2"
	if err := c.PatchConfig(ctx, "default", "c1", etag, map[string]*string{"user.a": nil, "user.b": &value}); err != nil {
		t.Fatal(err)
	}
	if config, _ := fake.Config("default", "c1"); config["user.b"] != "2" || config["user.a"] != "" {
		t.Errorf("config %v", config)
	}
	if err := c.PatchConfig(ctx, "default", "c1", etag, map[string]*string{"user.b": nil}); KindOf(err) != KindPrecondition {
		t.Errorf("stale ETag: %v; want %s", err, KindPrecondition)
	}
	fake.PatchAsOperation("Success")
	if err := c.PatchConfig(ctx, "default", "c1", "", map[string]*string{"user.b": nil}); err != nil {
		t.Errorf("operation: %v", err)
	}
	fake.PatchAsOperation("Failure")
	if err := c.PatchConfig(ctx, "default", "c1", "", map[string]*string{"user.b": nil}); KindOf(err) != KindServer || !strings.Contains(err.Error(), "Failure") {
		t.Errorf("failed operation: %v; want %s", err, KindServer)
	}
}

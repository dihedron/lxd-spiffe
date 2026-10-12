package devlxd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
)

// serve starts an HTTP server on a Unix socket in a temporary directory and
// returns the socket's path. The handlers answer as the test needs; they do
// not claim to be how devLXD answers (the probe's fixtures say that).
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "devlxd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	sock := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go srv.Serve(l)                   //nolint:errcheck
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck
	return sock
}

func connect(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const (
	plainServer    = `{"api_version":"1.0","instance_type":"container","location":"lxd5","state":"Started"}`
	envelopeServer = `{"type":"sync","status":"Success","status_code":200,"metadata":` + plainServer + `}`
)

func TestGetFormats(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0":
			w.Write([]byte(plainServer)) //nolint:errcheck
		case "/envelope":
			w.Write([]byte(envelopeServer)) //nolint:errcheck
		case "/1.0/config/user.lxd-probe.a":
			w.Write([]byte("value")) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not found")) //nolint:errcheck
		}
	}))
	c := connect(t, Config{Socket: sock})
	ctx := context.Background()

	for _, path := range []string{"/1.0", "/envelope"} {
		resp, err := c.Get(ctx, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want := FormatPlain
		if path == "/envelope" {
			want = FormatEnvelope
		}
		if resp.Status != http.StatusOK || resp.Format != want {
			t.Errorf("%s: status %d, format %s; want 200, %s", path, resp.Status, resp.Format, want)
		}
		var srv api.DevLXDGet
		if err := resp.Decode(&srv); err != nil || srv.InstanceType != "container" || srv.Location != "lxd5" {
			t.Errorf("%s: decoded %+v, %v", path, srv, err)
		}
	}

	resp, err := c.Get(ctx, "/1.0/config/user.lxd-probe.a")
	if err != nil || resp.Format != FormatPlain || string(resp.Body) != "value" {
		t.Errorf("text body: %+v, %v", resp, err)
	}
	var v any
	if err := resp.Decode(&v); err == nil {
		t.Errorf("a text body decoded as JSON: %v", v)
	}

	// an error status is a response, not an error; decoding it is
	resp, err = c.Get(ctx, "/missing")
	if err != nil || resp.Status != http.StatusNotFound || string(resp.Body) != "not found" {
		t.Fatalf("404: %+v, %v", resp, err)
	}
	if err := resp.Decode(&v); lxd.KindOf(err) != lxd.KindNotFound {
		t.Errorf("decoding a 404: %v (kind %q)", err, lxd.KindOf(err))
	}
}

func TestDetectFormat(t *testing.T) {
	for body, want := range map[string]Format{
		plainServer:    FormatPlain,
		envelopeServer: FormatEnvelope,
		`{"type":"error","error":"not found","error_code":404,"metadata":null}`: FormatEnvelope,
		`{"type":"container"}`: FormatPlain, // a "type" field alone is not an envelope
		`text`:                 FormatPlain,
		``:                     FormatPlain,
	} {
		if got := DetectFormat([]byte(body)); got != want {
			t.Errorf("DetectFormat(%q) = %s; want %s", body, got, want)
		}
	}
}

func TestLimits(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Write([]byte(strings.Repeat("x", 100))) //nolint:errcheck
		case "/slow":
			time.Sleep(500 * time.Millisecond)
			w.Write([]byte("late")) //nolint:errcheck
		}
	}))
	c := connect(t, Config{Socket: sock, MaxBytes: 64, Timeout: 100 * time.Millisecond})
	if _, err := c.Get(context.Background(), "/big"); lxd.KindOf(err) != lxd.KindTooLarge {
		t.Errorf("body over the limit: %v", err)
	}
	if _, err := c.Get(context.Background(), "/slow"); lxd.KindOf(err) != lxd.KindTimeout {
		t.Errorf("slow answer: %v (kind %q)", err, lxd.KindOf(err))
	}
	// a shorter deadline in the context wins
	c = connect(t, Config{Socket: sock})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, "/slow"); lxd.KindOf(err) != lxd.KindTimeout {
		t.Errorf("context deadline: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	var agent string
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.UserAgent()
		w.Write([]byte(strings.Repeat("x", DefaultMaxBytes))) //nolint:errcheck
	}))
	c := connect(t, Config{Socket: sock})
	if c.timeout != DefaultTimeout {
		t.Errorf("timeout %v; want %v (FR-A3)", c.timeout, DefaultTimeout)
	}
	resp, err := c.Get(context.Background(), "/1.0")
	if err != nil || len(resp.Body) != DefaultMaxBytes {
		t.Errorf("a body of exactly the limit: %v", err)
	}
	if !strings.HasPrefix(agent, "lxd-spiffe/") {
		t.Errorf("user agent %q", agent)
	}
}

func TestRecorder(t *testing.T) {
	sock := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "1")
		w.Write([]byte(plainServer)) //nolint:errcheck
	}))
	var got []lxd.Exchange
	c := connect(t, Config{Socket: sock, Recorder: func(x lxd.Exchange) { got = append(got, x) }})
	if _, err := c.Get(context.Background(), "/1.0/config"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/1.0/config" ||
		got[0].Status != http.StatusOK || string(got[0].ResponseBody) != plainServer || got[0].ResponseHeader.Get("X-Test") != "1" {
		t.Errorf("recorded %+v", got)
	}
}

func TestConnectErrors(t *testing.T) {
	if _, err := Connect(context.Background(), Config{Socket: filepath.Join(t.TempDir(), "absent")}); lxd.KindOf(err) != lxd.KindConnection {
		t.Errorf("missing socket: %v (kind %q)", err, lxd.KindOf(err))
	}
	notSocket := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(context.Background(), Config{Socket: notSocket}); lxd.KindOf(err) != lxd.KindConnection {
		t.Errorf("not a socket: %v", err)
	}
	var e *lxd.Error
	if _, err := Connect(context.Background(), Config{Socket: notSocket}); !errors.As(err, &e) {
		t.Errorf("not an *lxd.Error: %T", err)
	}
}

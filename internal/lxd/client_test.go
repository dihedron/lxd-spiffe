package lxd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canonical/lxd/shared/api"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// clientCert writes a self-signed client certificate and key into dir and
// returns their paths and the certificate's SHA-256 fingerprint.
func clientCert(t *testing.T, dir, name string) (certFile, keyFile, fingerprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, name+".crt")
	keyFile = filepath.Join(dir, name+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return certFile, keyFile, hex.EncodeToString(sum[:])
}

// testToken is JWT-shaped, as LXD bearer tokens are: the SDK parses the
// token to look for a server fingerprint claim and refuses anything else.
const testToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJwcm9iZSJ9.c2lnbmF0dXJl"

func connect(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func fakeConfig(t *testing.T, fake *fakelxd.Server) Config {
	t.Helper()
	cert, key, _ := clientCert(t, t.TempDir(), "probe")
	return Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), ClientCertFile: cert, ClientKeyFile: key, Project: "default"}
}

func TestConnectConfig(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{Endpoint: "https://lab:8443"},     // no pin
		{Endpoint: "ftp://lab", Pin: "ab"}, // scheme
		{Endpoint: "https://lab:8443", Pin: "not-hex"},  // pin format
		{Endpoint: "unix:///run/lxd.sock", Project: ""}, // no project
		{Endpoint: "https://lab:8443", Pin: "ab", Project: "p", ClientCertFile: "only-cert"},
	} {
		if _, err := Connect(context.Background(), cfg); err == nil {
			t.Errorf("Connect(%+v) succeeded", cfg)
		}
	}
}

func TestNormalizePin(t *testing.T) {
	want := strings.Repeat("ab", 32)
	for _, pin := range []string{want, strings.ToUpper(want), "sha256:" + want, "SHA256:" + strings.ToUpper(want), strings.TrimSuffix(strings.Repeat("AB:", 32), ":")} {
		got, err := normalizePin(pin)
		if err != nil || got != want {
			t.Errorf("normalizePin(%q) = %q, %v", pin, got, err)
		}
	}
	if _, err := normalizePin("abcd"); err == nil {
		t.Errorf("short pin accepted")
	}
}

func TestPin(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	cfg := fakeConfig(t, fake)

	resp, err := connect(t, cfg).Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if err != nil {
		t.Fatalf("matching pin: %v", err)
	}
	if resp.Status != http.StatusOK || resp.TLSVersion != tls.VersionTLS13 {
		t.Errorf("status = %d, TLS version = %x", resp.Status, resp.TLSVersion)
	}

	cfg.Pin = strings.Repeat("00", 32)
	_, err = connect(t, cfg).Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if KindOf(err) != KindConnection {
		t.Errorf("pin mismatch: kind = %v (%v); want connection", KindOf(err), err)
	}
}

func TestProjectAndIfMatch(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddInstance("p1", "c1", map[string]string{"user.lxd-probe.a": "1"})
	cfg := fakeConfig(t, fake)
	cfg.Project = "p1"
	c := connect(t, cfg)

	if _, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"}); err != nil {
		t.Fatal(err)
	}
	get, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0/instances/c1"})
	if err != nil || get.Status != http.StatusOK || get.Header.Get("ETag") == "" {
		t.Fatalf("GET instance: %v, %+v", err, get)
	}
	patch, err := c.Raw(context.Background(), RawRequest{
		Method: http.MethodPatch, Path: "/1.0/instances/c1",
		Body: []byte(`{"config":{"user.lxd-probe.a":"2"}}`), ETag: get.Header.Get("ETag"),
	})
	if err != nil || patch.Status != http.StatusAccepted {
		t.Fatalf("PATCH: %v, %+v", err, patch)
	}
	stale, err := c.Raw(context.Background(), RawRequest{
		Method: http.MethodPatch, Path: "/1.0/instances/c1",
		Body: []byte(`{"config":{"user.lxd-probe.a":"3"}}`), ETag: get.Header.Get("ETag"),
	})
	if err != nil || StatusKind(stale.Status) != KindPrecondition {
		t.Fatalf("stale PATCH: %v, status %d", err, stale.Status)
	}

	for _, r := range fake.Requests() {
		if r.Query.Get("project") != "p1" {
			t.Errorf("%s %s without project=p1: %v", r.Method, r.Path, r.Query)
		}
		if r.Method == http.MethodPatch && r.Header.Get("If-Match") != get.Header.Get("ETag") {
			t.Errorf("PATCH If-Match = %q", r.Header.Get("If-Match"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "lxd-spiffe") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
	}
}

func TestStatusKind(t *testing.T) {
	for status, want := range map[int]Kind{
		200: KindNone, 202: KindNone, 401: KindAuth, 403: KindAuth, 404: KindNotFound,
		412: KindPrecondition, 409: KindServer, 500: KindServer, 302: KindRedirect,
	} {
		if got := StatusKind(status); got != want {
			t.Errorf("StatusKind(%d) = %v; want %v", status, got, want)
		}
	}
}

func TestRedirectRefused(t *testing.T) {
	var hit sync.Mutex
	followed := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Lock()
		followed = true
		hit.Unlock()
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/1.0", http.StatusFound)
	}))
	defer redirect.Close()

	sum := sha256.Sum256(redirect.Certificate().Raw)
	var recorded []Exchange
	c := connect(t, Config{
		Endpoint: redirect.URL, Pin: hex.EncodeToString(sum[:]), Project: "default", BearerToken: testToken,
		Recorder: func(e Exchange) { recorded = append(recorded, e) },
	})
	_, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if KindOf(err) != KindRedirect {
		t.Errorf("kind = %v (%v); want redirect", KindOf(err), err)
	}
	if followed {
		t.Errorf("redirect followed")
	}
	if len(recorded) != 1 || recorded[0].Status != http.StatusFound {
		t.Errorf("recorded = %+v; want the 302", recorded)
	}
}

func TestBodyLimits(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"sync","metadata":"` + big + `"}`)) //nolint:errcheck
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c := connect(t, Config{Endpoint: srv.URL, Pin: hex.EncodeToString(sum[:]), Project: "default"})

	_, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if KindOf(err) != KindTooLarge {
		t.Errorf("default limit: kind = %v (%v)", KindOf(err), err)
	}
	_, err = c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0", MaxBytes: 4 << 20})
	if err != nil {
		t.Errorf("raised limit: %v", err)
	}
	_, err = c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0", MaxBytes: 16})
	if KindOf(err) != KindTooLarge {
		t.Errorf("small limit: kind = %v (%v)", KindOf(err), err)
	}
}

func TestClientCertReload(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	dir := t.TempDir()
	certA, keyA, fpA := clientCert(t, dir, "a")
	c := connect(t, Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), ClientCertFile: certA, ClientKeyFile: keyA, Project: "default"})
	if _, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"}); err != nil {
		t.Fatal(err)
	}
	// rotate: the files now hold certificate b
	certB, keyB, fpB := clientCert(t, t.TempDir(), "b")
	for src, dst := range map[string]string{certB: certA, keyB: keyA} {
		data, _ := os.ReadFile(src)
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(time.Second)
	os.Chtimes(certA, future, future) //nolint:errcheck
	if _, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"}); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if reqs[0].ClientCert != fpA || reqs[1].ClientCert != fpB {
		t.Errorf("client certs = %s, %s; want %s, %s", reqs[0].ClientCert, reqs[1].ClientCert, fpA, fpB)
	}
}

func TestBearerToken(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	cfg := fakeConfig(t, fake)
	cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
	cfg.BearerToken = testToken
	if _, err := connect(t, cfg).Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests()[0].Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
}

func TestReadSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	for mode, ok := range map[os.FileMode]bool{0o600: true, 0o400: true, 0o640: false, 0o604: false, 0o644: false} {
		os.Remove(path) //nolint:errcheck
		if err := os.WriteFile(path, []byte("tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, mode) //nolint:errcheck
		got, err := ReadSecretFile(path)
		if (err == nil) != ok {
			t.Errorf("mode %o: err = %v; want ok=%v", mode, err, ok)
		}
		if ok && got != "tok" {
			t.Errorf("mode %o: token = %q", mode, got)
		}
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c := connect(t, Config{Endpoint: srv.URL, Pin: hex.EncodeToString(sum[:]), Project: "default"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Raw(ctx, RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if KindOf(err) != KindTimeout {
		t.Errorf("kind = %v (%v); want timeout", KindOf(err), err)
	}
}

func TestUnixEndpoint(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var gotQuery url.Values
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"api_version":"1.0"}}`)) //nolint:errcheck
	})}
	go srv.Serve(l) //nolint:errcheck
	defer srv.Close()

	c := connect(t, Config{Endpoint: "unix://" + sock, Project: "p1"})
	resp, err := c.Raw(context.Background(), RawRequest{Method: http.MethodGet, Path: "/1.0"})
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("unix GET: %v, %+v", err, resp)
	}
	if gotQuery.Get("project") != "p1" {
		t.Errorf("query = %v", gotQuery)
	}
	var server api.Server
	if err := Decode(resp, &server); err != nil || server.APIVersion != "1.0" {
		t.Errorf("Decode: %v, %+v", err, server)
	}
}

func TestDecode(t *testing.T) {
	sync := &RawResponse{Status: 200, Body: []byte(`{"type":"sync","metadata":{"name":"c1","status":"Running"}}`)}
	var inst api.Instance
	if err := Decode(sync, &inst); err != nil || inst.Name != "c1" {
		t.Errorf("sync: %v, %+v", err, inst)
	}

	notFound := &RawResponse{Status: 404, Body: []byte(`{"type":"error","error":"Instance not found","error_code":404}`)}
	err := Decode(notFound, &inst)
	if KindOf(err) != KindNotFound {
		t.Errorf("error envelope: kind = %v (%v)", KindOf(err), err)
	}

	async := &RawResponse{Status: 202, Body: []byte(`{"type":"async","operation":"/1.0/operations/op-1","metadata":{"id":"op-1"}}`)}
	env, err := async.Envelope()
	if err != nil || env.Type != api.AsyncResponse || env.Operation != "/1.0/operations/op-1" {
		t.Errorf("async: %v, %+v", err, env)
	}

	garbage := &RawResponse{Status: 200, Body: []byte(`<html>`)}
	if err := Decode(garbage, &inst); KindOf(err) != KindProtocol {
		t.Errorf("garbage: kind = %v (%v)", KindOf(err), err)
	}
}

func TestKindOf(t *testing.T) {
	if KindOf(nil) != KindNone {
		t.Errorf("KindOf(nil) = %v", KindOf(nil))
	}
	if KindOf(errors.New("x")) != KindOther {
		t.Errorf("KindOf(plain) = %v", KindOf(errors.New("x")))
	}
	wrapped := errors.Join(errors.New("ctx"), &Error{Kind: KindAuth, Err: errors.New("denied")})
	if KindOf(wrapped) != KindAuth {
		t.Errorf("KindOf(wrapped) = %v", KindOf(wrapped))
	}
}

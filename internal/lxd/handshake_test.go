package lxd

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tlsServer starts a TLS server limited to versions min..max and returns it
// with its certificate's fingerprint.
func tlsServer(t *testing.T, min, max uint16) (*httptest.Server, string) {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ProbeTLS sent an HTTP request: %s %s", r.Method, r.URL)
	}))
	s.TLS = &tls.Config{MinVersion: min, MaxVersion: max}
	s.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	s.StartTLS()
	t.Cleanup(s.Close)
	sum := sha256.Sum256(s.Certificate().Raw)
	return s, hex.EncodeToString(sum[:])
}

// TestProbeTLS12 runs the --probe-tls12 handshake against a server limited
// to TLS 1.2 and one limited to TLS 1.3 (PRT-11).
func TestProbeTLS12(t *testing.T) {
	ctx := context.Background()

	tls12, pin12 := tlsServer(t, tls.VersionTLS12, tls.VersionTLS12)
	h, err := ProbeTLS(ctx, tls12.URL, pin12, tls.VersionTLS12, tls.VersionTLS12)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Accepted || h.Version != tls.VersionTLS12 || h.CipherSuite == 0 || !h.PinMatch {
		t.Errorf("TLS 1.2 server: %+v; want accepted with TLS 1.2, a cipher and the pin matched", h)
	}

	tls13, pin13 := tlsServer(t, tls.VersionTLS13, tls.VersionTLS13)
	h, err = ProbeTLS(ctx, tls13.URL, pin13, tls.VersionTLS12, tls.VersionTLS12)
	if err != nil {
		t.Fatal(err)
	}
	if h.Accepted || h.Err == nil {
		t.Errorf("TLS 1.3 server: %+v; want the handshake refused with a reason", h)
	}
}

func TestProbeTLSPinMismatch(t *testing.T) {
	s, _ := tlsServer(t, tls.VersionTLS12, tls.VersionTLS13)
	h, err := ProbeTLS(context.Background(), s.URL, strings.Repeat("ab", 32), tls.VersionTLS12, tls.VersionTLS12)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Accepted || h.PinMatch {
		t.Errorf("%+v; want accepted, pin not matched", h)
	}
}

func TestProbeTLSErrors(t *testing.T) {
	ctx := context.Background()
	pin := strings.Repeat("ab", 32)
	for _, endpoint := range []string{"unix:///run/lxd.sock", "http://lab:8443"} {
		if _, err := ProbeTLS(ctx, endpoint, pin, tls.VersionTLS12, tls.VersionTLS12); err == nil {
			t.Errorf("ProbeTLS(%q) succeeded", endpoint)
		}
	}
	if _, err := ProbeTLS(ctx, "https://lab:8443", "short", tls.VersionTLS12, tls.VersionTLS12); err == nil {
		t.Errorf("ProbeTLS accepted a bad pin")
	}
	s, _ := tlsServer(t, tls.VersionTLS12, tls.VersionTLS13)
	addr := s.URL
	s.Close()
	_, err := ProbeTLS(ctx, addr, pin, tls.VersionTLS12, tls.VersionTLS12)
	if KindOf(err) != KindConnection {
		t.Errorf("closed server: kind %q (%v); want %q", KindOf(err), err, KindConnection)
	}
}

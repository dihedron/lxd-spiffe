package lxdinstance

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// otherToken is a second signed token, as a rotation issues.
const otherToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJzcGlyZTIifQ.c2lnbmF0dXJl"

func fakeServer(t *testing.T, fake *fakelxd.Server, f testFiles, bearer bool) *lxdServer {
	t.Helper()
	sc := ServerConfig{URL: fake.URL, ServerCertFingerprint: fake.Fingerprint(), Projects: []string{"default"}}
	if bearer {
		sc.BearerTokenFile = f.token
	} else {
		sc.ClientCertFile, sc.ClientKeyFile = f.cert, f.key
	}
	s, err := newLXDServer(context.Background(), "lab", sc, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// serverReads counts the fake's GET /1.0, and returns the last one's
// Authorization header.
func serverReads(fake *fakelxd.Server) (int, string) {
	n, auth := 0, ""
	for _, r := range fake.Requests() {
		if r.Method == "GET" && r.Path == "/1.0" {
			n, auth = n+1, r.Header.Get("Authorization")
		}
	}
	return n, auth
}

// TestFRS12CheckedOnce: the first use reads GET /1.0 and checks it; later
// uses do not read it again.
func TestFRS12CheckedOnce(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions(lxd.RequiredExtensions...)
	s := fakeServer(t, fake, newTestFiles(t), false)
	for range 3 {
		if _, err := s.checked(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := serverReads(fake); n != 1 {
		t.Errorf("%d reads of GET /1.0, want 1", n)
	}
}

// TestFRS12RefusedServer: a server without the required extensions is
// refused, every time, and checked again on the next use.
func TestFRS12RefusedServer(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions("projects", "instance_generation_id")
	s := fakeServer(t, fake, newTestFiles(t), false)
	for range 2 {
		if _, err := s.checked(context.Background()); err == nil || !strings.Contains(err.Error(), "api_filtering") {
			t.Fatalf("missing api_filtering: %v", err)
		}
	}
	if n, _ := serverReads(fake); n != 2 {
		t.Errorf("%d reads of GET /1.0, want 2", n)
	}
}

// TestFRS19BearerNeedsExtension: a bearer entry also needs auth_bearer.
func TestFRS19BearerNeedsExtension(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions(lxd.RequiredExtensions...)
	s := fakeServer(t, fake, newTestFiles(t), true)
	if _, err := s.checked(context.Background()); err == nil || !strings.Contains(err.Error(), "auth_bearer") {
		t.Fatalf("missing auth_bearer: %v", err)
	}
}

// TestFRS19TokenRotation: a changed token file is read again and the client
// reconnects with the new token, checked again.
func TestFRS19TokenRotation(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions(append(lxd.RequiredExtensions, "auth_bearer")...)
	f := newTestFiles(t)
	s := fakeServer(t, fake, f, true)
	if _, err := s.checked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.token, []byte(otherToken), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(f.token, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := s.checked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, auth := serverReads(fake); n != 2 || auth != "Bearer "+otherToken {
		t.Errorf("%d reads of GET /1.0, last with %q; want 2, the new token", n, auth)
	}
}

// TestFRS19RereadAfter403: a 403 on GET /1.0 makes the token be read again
// once, then the server fails closed.
func TestFRS19RereadAfter403(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.SetExtensions(append(lxd.RequiredExtensions, "auth_bearer")...)
	fake.ExpireBearerAt(time.Now().Add(-time.Second))
	s := fakeServer(t, fake, newTestFiles(t), true)
	_, err := s.checked(context.Background())
	if lxd.KindOf(err) != lxd.KindAuth {
		t.Fatalf("expired token: kind %q (%v)", lxd.KindOf(err), err)
	}
	if n, _ := serverReads(fake); n != 2 {
		t.Errorf("%d reads of GET /1.0, want 2 (one after re-reading the token)", n)
	}
}

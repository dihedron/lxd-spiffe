package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

const (
	testIssuer  = "lxd:01a12c24-43bb-781a-ad3a-36b2490f435c"
	testSubject = "5f0e8d4c-1b2a-4c3d-9e8f-7a6b5c4d3e2f"
)

// jwtToken builds an unsigned-looking JWT shaped as LXD's bearer tokens:
// HS512, iss and aud naming the server, sub the identity, and iat, nbf and
// exp. The signature is a placeholder: the probe never verifies it.
func jwtToken(t *testing.T, iat, exp time.Time) string {
	t.Helper()
	enc := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	header := enc(map[string]string{"alg": "HS512", "typ": "JWT"})
	claims := enc(map[string]any{
		"iss": testIssuer, "aud": []string{testIssuer}, "sub": testSubject,
		"iat": iat.Unix(), "nbf": iat.Unix(), "exp": exp.Unix(),
	})
	return header + "." + claims + "." + base64.RawURLEncoding.EncodeToString([]byte("signature-placeholder"))
}

// bearerCheck runs server bearer check with token against fake.
func bearerCheck(t *testing.T, fake *fakelxd.Server, token string, setup func(*BearerCheck)) (*BearerCheck, evidence.Record, error) {
	t.Helper()
	opts := options(t, fake)
	opts.Auth = "bearer"
	opts.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(opts.TokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &BearerCheck{ServerCommand: opts, MaxWait: time.Hour, Interval: time.Second, stdout: &bytes.Buffer{}}
	if setup != nil {
		setup(cmd)
	}
	err := cmd.Execute(nil)
	return cmd, record(t, cmd.ServerCommand, 1), err
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%v is not an object: %v", v, err)
	}
	return m
}

func TestBearerCheckClaims(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	iat := time.Now().Add(-time.Minute).Truncate(time.Second)
	exp := iat.Add(2 * time.Minute)
	token := jwtToken(t, iat, exp)
	cmd, rec, err := bearerCheck(t, fake, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Verb != "bearer check" || rec.Outcome != evidence.OutcomeOK {
		t.Errorf("record %q %s", rec.Verb, rec.Outcome)
	}
	if got := observation(t, rec, "token.format"); got != "jwt" {
		t.Errorf("token.format = %v", got)
	}
	header := asMap(t, observation(t, rec, "token.header"))
	if header["alg"] != "HS512" || header["typ"] != "JWT" {
		t.Errorf("token.header = %v", header)
	}
	claims := asMap(t, observation(t, rec, "token.claims"))
	want := map[string]any{
		"iss": testIssuer, "iat": iat.UTC().Format(time.RFC3339), "nbf": iat.UTC().Format(time.RFC3339),
		"exp": exp.UTC().Format(time.RFC3339), "lifetime_s": float64(120), "sub_present": true,
		"server_cert_fingerprint_present": false,
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("token.claims[%s] = %v; want %v", k, claims[k], v)
		}
	}
	if aud, _ := claims["aud"].([]any); len(aud) != 1 || aud[0] != testIssuer {
		t.Errorf("token.claims[aud] = %v", claims["aud"])
	}
	calls := asMap(t, observation(t, rec, "bearer.calls"))
	if calls["server"] != "allowed" || calls["identity"] != "allowed" {
		t.Errorf("bearer.calls = %v", calls)
	}
	// neither the token, its signature nor the subject reach the evidence
	parts := bytes.Split([]byte(token), []byte("."))
	for _, secret := range []string{token, string(parts[2]), testSubject} {
		assertAbsent(t, secret, cmd.EvidenceDir)
	}
}

func TestBearerCheckWaitExpiry(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	now := time.Now()
	// JWT times are whole seconds: the fake refuses from the claimed exp
	exp := now.Add(1500 * time.Millisecond).Truncate(time.Second).Add(time.Second)
	fake.ExpireBearerAt(exp)
	_, rec, err := bearerCheck(t, fake, jwtToken(t, now, exp), func(c *BearerCheck) {
		c.WaitExpiry, c.Interval, c.MaxWait = true, 100*time.Millisecond, 10*time.Second
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeOK {
		t.Errorf("outcome %s; want ok", rec.Outcome)
	}
	refusal := asMap(t, observation(t, rec, "expiry.first_refusal"))
	if refusal["status"] != float64(403) || refusal["error"] == "" {
		t.Errorf("expiry.first_refusal = %v", refusal)
	}
	offset, _ := refusal["offset_from_exp_s"].(float64)
	if offset < 0 || offset > 1 {
		t.Errorf("refusal %v s after exp; want within one 100 ms interval or so", offset)
	}
	if _, ok := asMap(t, observation(t, rec, "expiry.last_success"))["at"]; !ok {
		t.Error("expiry.last_success has no time")
	}
}

func TestBearerCheckMaxWait(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	now := time.Now()
	_, rec, err := bearerCheck(t, fake, jwtToken(t, now, now.Add(time.Hour)), func(c *BearerCheck) {
		c.WaitExpiry, c.Interval, c.MaxWait = true, 50*time.Millisecond, 300*time.Millisecond
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("outcome %s; want unexpected when --max-wait passes without a refusal", rec.Outcome)
	}
	if got := observation(t, rec, "expiry.not_reached"); got == nil {
		t.Error("no expiry.not_reached")
	}
}

func TestBearerCheckRefused(t *testing.T) {
	now := time.Now()
	token := jwtToken(t, now.Add(-time.Hour), now.Add(-time.Minute))

	fake := fakelxd.NewAssumed(t)
	fake.ExpireBearerAt(now.Add(-time.Minute))
	_, rec, err := bearerCheck(t, fake, token, nil)
	if exit.Code(err) != exit.Auth {
		t.Fatalf("refused token: exit %d (%v); want %d", exit.Code(err), err, exit.Auth)
	}
	if calls := asMap(t, observation(t, rec, "bearer.calls")); calls["server"] != "denied" {
		t.Errorf("bearer.calls = %v", calls)
	}

	// a revoked token is what the runbook expects: --expect-denied
	fake = fakelxd.NewAssumed(t)
	fake.ExpireBearerAt(now.Add(-time.Minute))
	_, rec, err = bearerCheck(t, fake, token, func(c *BearerCheck) { c.ExpectDenied = true })
	if err != nil {
		t.Fatalf("--expect-denied: %v", err)
	}
	if rec.Outcome != evidence.OutcomeOK {
		t.Errorf("--expect-denied outcome %s", rec.Outcome)
	}

	// and a token that still works under --expect-denied is a finding
	fake = fakelxd.NewAssumed(t)
	_, rec, err = bearerCheck(t, fake, jwtToken(t, now, now.Add(time.Hour)), func(c *BearerCheck) { c.ExpectDenied = true })
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != evidence.OutcomeUnexpected {
		t.Errorf("accepted token under --expect-denied: outcome %s", rec.Outcome)
	}
}

func TestBearerCheckUsage(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	tlsOpts := options(t, fake)
	cmd := &BearerCheck{ServerCommand: tlsOpts, MaxWait: time.Hour, Interval: time.Second, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("--auth tls: exit %d (%v); want %d", exit.Code(err), err, exit.Usage)
	}
	// the SDK parses the token when connecting: one that is not a JWT
	// never reaches a call
	opts := options(t, fake)
	opts.Auth = "bearer"
	opts.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(opts.TokenFile, []byte("opaque-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = &BearerCheck{ServerCommand: opts, MaxWait: time.Hour, Interval: time.Second, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("not a JWT: exit %d (%v); want %d", exit.Code(err), err, exit.Usage)
	}
	if len(fake.Requests()) != 0 {
		t.Errorf("not a JWT: %d requests sent", len(fake.Requests()))
	}
}

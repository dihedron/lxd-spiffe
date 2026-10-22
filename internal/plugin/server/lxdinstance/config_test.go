package lxdinstance

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// signedToken is a JWT with a signature part, as LXD issues them; its
// signature is LXD's to check, not the plugin's.
const signedToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJzcGlyZSJ9.c2lnbmF0dXJl"

// testFiles are the credential files of a test configuration.
type testFiles struct{ cert, key, token, ca string }

func newTestFiles(t *testing.T) testFiles {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "spire"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	f := testFiles{cert: filepath.Join(dir, "client.crt"), key: filepath.Join(dir, "client.key"), token: filepath.Join(dir, "bearer.token"), ca: filepath.Join(dir, "ca.pem")}
	write := func(path string, data []byte, mode os.FileMode) {
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	write(f.cert, certPEM, 0o644)
	write(f.key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	write(f.token, []byte(signedToken+"\n"), 0o600)
	write(f.ca, certPEM, 0o644)
	return f
}

var testPin = "sha256:" + strings.Repeat("abcdef0123456789", 4)

// hclConfig is a valid server.conf plugin_data block, with top-level lines
// and the lines of its one server entry, "lxd-prod", replaceable.
func hclConfig(f testFiles, top, server string) string {
	if server == "" {
		server = fmt.Sprintf(`url = "https://lxd.example.org:8443"
      client_cert_file = %q
      client_key_file = %q
      server_cert_fingerprint = %q
      projects = ["spire-nodes"]`, f.cert, f.key, testPin)
	}
	return top + "\nservers = {\n  \"lxd-prod\" = {\n      " + server + "\n  }\n}\n"
}

func configure(t *testing.T, hcl string) (*Plugin, error) {
	t.Helper()
	p := New()
	_, err := p.Configure(context.Background(), &configv1.ConfigureRequest{
		HclConfiguration:  hcl,
		CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org"},
	})
	return p, err
}

// TestConfigureDefaults: a minimal configuration gets the defaults of the
// spec's server.conf.
func TestConfigureDefaults(t *testing.T) {
	p, err := configure(t, hclConfig(newTestFiles(t), "", ""))
	if err != nil {
		t.Fatal(err)
	}
	s := p.current()
	if s.mode != "file_pull" || s.proofTimeout != 10*time.Second || s.proofDir != "/run/spire/lxd" || !s.reattest ||
		s.limits.maxInflight != 16 || s.limits.ratePerSec != 20 || s.limits.failureCooldown != 5*time.Second ||
		s.selectors.Enable != nil || s.trustDomain != "example.org" || s.devMode {
		t.Errorf("defaults: %+v", s)
	}
	if len(s.servers) != 1 || s.servers[0].id != "lxd-prod" || len(s.servers[0].projects) != 1 {
		t.Fatalf("servers: %+v", s.servers)
	}
	path, err := s.agentPath("lxd-prod", "fbf73246-b07c-4291-afd9-12ae2be70828")
	if err != nil || path != "lxd_instance/lxd-prod/fbf73246-b07c-4291-afd9-12ae2be70828" {
		t.Errorf("FR-S7 default path %q, %v", path, err)
	}
}

// TestConfigureAccepts: the other valid shapes of a server entry and of the
// top-level keys.
func TestConfigureAccepts(t *testing.T) {
	f := newTestFiles(t)
	for name, hcl := range map[string]string{
		"FR-S19 bearer token": hclConfig(f, "", fmt.Sprintf(`url = "https://lxd.example.org:8443"
      bearer_token_file = %q
      server_cert_fingerprint = %q
      projects = ["a", "b"]`, f.token, testPin)),
		"SEC-07 CA bundle": hclConfig(f, "", fmt.Sprintf(`url = "https://lxd.example.org:8443/"
      client_cert_file = %q
      client_key_file = %q
      ca_bundle_file = %q
      projects = ["a"]`, f.cert, f.key, f.ca)),
		"SEC-07 dev_mode without verification": hclConfig(f, "dev_mode = true", fmt.Sprintf(`url = "https://lxd.example.org:8443"
      client_cert_file = %q
      client_key_file = %q
      projects = ["a"]`, f.cert, f.key)),
		"SEC-02 none acknowledged": hclConfig(f, "proof_mode = \"none\"\ni_understand_claim_only_is_insecure = true", ""),
		"every block": hclConfig(f, `proof_mode = "auto"
proof_timeout = "90s"
proof_dir = "/run/spire/proof"
reattest = "deny"
agent_path_template = "{{ .PluginName }}/{{ .ServerID }}/node/{{ .InstanceUUID }}"
policy {
  allow_privileged = true
  deny_config_keys = ["raw.lxc", "raw.idmap"]
}
selectors {
  enable = ["server", "uuid"]
}
limits {
  max_inflight = 4
  rate_per_sec = 2
  failure_cooldown = "1s"
}`, ""),
	} {
		if _, err := configure(t, hcl); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestConfigureRefuses: each rule of Validation at Configure, on its own.
func TestConfigureRefuses(t *testing.T) {
	f := newTestFiles(t)
	server := func(lines ...string) string { return strings.Join(lines, "\n      ") }
	url := `url = "https://lxd.example.org:8443"`
	cert := fmt.Sprintf("client_cert_file = %q", f.cert)
	key := fmt.Sprintf("client_key_file = %q", f.key)
	pin := fmt.Sprintf("server_cert_fingerprint = %q", testPin)
	projects := `projects = ["spire-nodes"]`
	loose := filepath.Join(t.TempDir(), "loose.key")
	if err := os.WriteFile(loose, []byte("key"), 0o644); err != nil {
		t.Fatal(err)
	}
	unsigned := filepath.Join(t.TempDir(), "unsigned.token")
	alg := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	if err := os.WriteFile(unsigned, []byte(alg+signedToken[strings.Index(signedToken, "."):strings.LastIndex(signedToken, ".")+1]), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown key":                     hclConfig(f, `proof_modes = "file_pull"`, ""),
		"unknown server key":              hclConfig(f, "", server(url, cert, key, pin, projects, `project = "x"`)),
		"FR-S11 no server":                `proof_mode = "file_pull"`,
		"FR-S11 server id not a segment":  strings.Replace(hclConfig(f, "", ""), `"lxd-prod"`, `"lxd/prod"`, 1),
		"FR-S11 server id dot-dot":        strings.Replace(hclConfig(f, "", ""), `"lxd-prod"`, `".."`, 1),
		"url not https":                   hclConfig(f, "", server(`url = "http://lxd.example.org:8443"`, cert, key, pin, projects)),
		"url with a path":                 hclConfig(f, "", server(`url = "https://lxd.example.org/1.0"`, cert, key, pin, projects)),
		"url with user info":              hclConfig(f, "", server(`url = "https://u:p@lxd.example.org"`, cert, key, pin, projects)),
		"FR-S19 no credential":            hclConfig(f, "", server(url, pin, projects)),
		"FR-S19 two credentials":          hclConfig(f, "", server(url, cert, key, fmt.Sprintf("bearer_token_file = %q", f.token), pin, projects)),
		"certificate without key":         hclConfig(f, "", server(url, cert, pin, projects)),
		"SEC-08 key readable by others":   hclConfig(f, "", server(url, cert, fmt.Sprintf("client_key_file = %q", loose), pin, projects)),
		"SEC-08 missing key":              hclConfig(f, "", server(url, cert, `client_key_file = "/nonexistent/key"`, pin, projects)),
		"SEC-08 token readable by others": hclConfig(f, "", server(url, fmt.Sprintf("bearer_token_file = %q", loose), pin, projects)),
		"FR-S19 unsigned token":           hclConfig(f, "", server(url, fmt.Sprintf("bearer_token_file = %q", unsigned), pin, projects)),
		"SEC-07 no verification":          hclConfig(f, "", server(url, cert, key, projects)),
		"SEC-07 pin and CA":               hclConfig(f, "", server(url, cert, key, pin, fmt.Sprintf("ca_bundle_file = %q", f.ca), projects)),
		"SEC-07 bad pin":                  hclConfig(f, "", server(url, cert, key, `server_cert_fingerprint = "sha256:abcd"`, projects)),
		"no projects":                     hclConfig(f, "", server(url, cert, key, pin, `projects = []`)),
		"invalid project":                 hclConfig(f, "", server(url, cert, key, pin, `projects = ["no spaces"]`)),
		"duplicate project":               hclConfig(f, "", server(url, cert, key, pin, `projects = ["a", "a"]`)),
		"unknown proof_mode":              hclConfig(f, `proof_mode = "ready_signal"`, ""),
		"SEC-02 none unacknowledged":      hclConfig(f, `proof_mode = "none"`, ""),
		"proof_timeout too short":         hclConfig(f, `proof_timeout = "0s"`, ""),
		"proof_timeout too long":          hclConfig(f, `proof_timeout = "1h"`, ""),
		"SEC-03 proof_timeout over 2m":    hclConfig(f, `proof_timeout = "121s"`, ""),
		"config_push not implemented yet": hclConfig(f, `proof_mode = "config_push"`, ""),
		"proof_timeout not a duration":    hclConfig(f, `proof_timeout = "ten"`, ""),
		"proof_dir relative":              hclConfig(f, `proof_dir = "run/spire"`, ""),
		"proof_dir root":                  hclConfig(f, `proof_dir = "/"`, ""),
		"proof_dir not clean":             hclConfig(f, `proof_dir = "/run/../spire"`, ""),
		"SEC-11 proof_dir too long":       hclConfig(f, `proof_dir = "/`+strings.Repeat("d", 120)+`"`, ""),
		"unknown reattest":                hclConfig(f, `reattest = "maybe"`, ""),
		"FR-S7 template syntax":           hclConfig(f, `agent_path_template = "{{ .ServerID"`, ""),
		"FR-S7 template unknown field":    hclConfig(f, `agent_path_template = "{{ .InstanceName }}"`, ""),
		"FR-S7 template bad path":         hclConfig(f, `agent_path_template = "{{ .ServerID }}/../{{ .InstanceUUID }}"`, ""),
		"FR-S7 template absolute":         hclConfig(f, `agent_path_template = "/{{ .InstanceUUID }}"`, ""),
		"FR-S8 unknown selector":          hclConfig(f, "selectors {\n  enable = [\"hostname\"]\n}", ""),
		"empty deny key":                  hclConfig(f, "policy {\n  deny_config_keys = [\"\"]\n}", ""),
		"SEC-09 max_inflight negative":    hclConfig(f, "limits {\n  max_inflight = -1\n}", ""),
		"SEC-09 rate too high":            hclConfig(f, "limits {\n  rate_per_sec = 100000\n}", ""),
		"SEC-09 cooldown too long":        hclConfig(f, "limits {\n  failure_cooldown = \"1h\"\n}", ""),
	}
	for name, hcl := range cases {
		if _, err := configure(t, hcl); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
}

// TestConfigureNeedsTrustDomain: the agent's SPIFFE ID needs the trust
// domain SPIRE passes in the core configuration (FR-S7).
func TestConfigureNeedsTrustDomain(t *testing.T) {
	p := New()
	_, err := p.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: hclConfig(newTestFiles(t), "", "")})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("no trust domain: %v", err)
	}
}

// TestConfigureReplaces: a second Configure replaces the first as a whole;
// a refused one leaves it in place.
func TestConfigureReplaces(t *testing.T) {
	f := newTestFiles(t)
	p, err := configure(t, hclConfig(f, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	req := func(hcl string) *configv1.ConfigureRequest {
		return &configv1.ConfigureRequest{HclConfiguration: hcl, CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org"}}
	}
	if _, err := p.Configure(ctx, req(hclConfig(f, `reattest = "deny"`, ""))); err != nil || p.current().reattest {
		t.Fatalf("replaced: %v, reattest %v", err, p.current().reattest)
	}
	if _, err := p.Configure(ctx, req(`bogus = 1`)); err == nil || p.current().reattest {
		t.Fatalf("a refused Configure changed the settings: %v", err)
	}
}

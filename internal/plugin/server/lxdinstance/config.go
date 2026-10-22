package lxdinstance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/plugin/config"
	"github.com/dihedron/lxd-spiffe/internal/policy"
	"github.com/dihedron/lxd-spiffe/internal/selectors"
)

// Config is the plugin_data block of the plugin in the SPIRE Server
// configuration (attestor spec, Configuration → Server).
type Config struct {
	AgentPathTemplate string                  `hcl:"agent_path_template"`
	ProofMode         string                  `hcl:"proof_mode"`
	ClaimOnlyAck      bool                    `hcl:"i_understand_claim_only_is_insecure"`
	ProofTimeout      string                  `hcl:"proof_timeout"`
	ProofDir          string                  `hcl:"proof_dir"`
	Reattest          string                  `hcl:"reattest"`
	Policy            *PolicyConfig           `hcl:"policy"`
	Servers           map[string]ServerConfig `hcl:"servers"`
	Selectors         *SelectorsConfig        `hcl:"selectors"`
	Limits            *LimitsConfig           `hcl:"limits"`
	DevMode           bool                    `hcl:"dev_mode"`
}

// PolicyConfig is the policy block (FR-S5).
type PolicyConfig struct {
	AllowPrivileged bool     `hcl:"allow_privileged"`
	DenyConfigKeys  []string `hcl:"deny_config_keys"`
}

// ServerConfig is one entry of servers; its key is the server_id (FR-S11).
type ServerConfig struct {
	URL                   string   `hcl:"url"`
	ClientCertFile        string   `hcl:"client_cert_file"`
	ClientKeyFile         string   `hcl:"client_key_file"`
	BearerTokenFile       string   `hcl:"bearer_token_file"`
	ServerCertFingerprint string   `hcl:"server_cert_fingerprint"`
	CABundleFile          string   `hcl:"ca_bundle_file"`
	Projects              []string `hcl:"projects"`
}

// SelectorsConfig is the selectors block (FR-S8).
type SelectorsConfig struct {
	Enable     []string `hcl:"enable"`
	ConfigKeys []string `hcl:"config_keys"`
	UserKeys   []string `hcl:"user_keys"`
}

// LimitsConfig is the limits block (SEC-09); zero means the default.
type LimitsConfig struct {
	MaxInflight     int    `hcl:"max_inflight"`
	RatePerSec      int    `hcl:"rate_per_sec"`
	FailureCooldown string `hcl:"failure_cooldown"`
}

// configKeys lists the keys Config accepts: any other key is an error.
var configKeys = []string{
	"agent_path_template", "proof_mode", "i_understand_claim_only_is_insecure", "proof_timeout", "proof_dir", "reattest", "dev_mode",
	"policy.allow_privileged", "policy.deny_config_keys",
	"servers.*.url", "servers.*.client_cert_file", "servers.*.client_key_file", "servers.*.bearer_token_file",
	"servers.*.server_cert_fingerprint", "servers.*.ca_bundle_file", "servers.*.projects",
	"selectors.enable", "selectors.config_keys", "selectors.user_keys",
	"limits.max_inflight", "limits.rate_per_sec", "limits.failure_cooldown",
}

// pluginName is the attestor's name, the first segment of the agent path.
const pluginName = "lxd_instance"

const (
	defaultPathTemplate = "{{ .PluginName }}/{{ .ServerID }}/{{ .InstanceUUID }}"
	defaultProofDir     = "/run/spire/lxd"
	// sampleUUID renders the path template at Configure.
	sampleUUID = "00000000-0000-4000-8000-000000000000"
)

// The proof modes of proof_mode.
const (
	modeFilePull   = "file_pull"
	modeConfigPush = "config_push"
	modeAuto       = "auto"
	modeNone       = "none"
)

// limitSettings are the limits of SEC-09.
type limitSettings struct {
	maxInflight     int
	ratePerSec      int
	failureCooldown time.Duration
}

// settings is a validated configuration; Configure replaces it as a whole.
type settings struct {
	trustDomain  string
	mode         string
	proofTimeout time.Duration
	proofDir     string
	reattest     bool
	pathTemplate *template.Template
	// policy is the policy of every server, Projects aside.
	policy    policy.Policy
	selectors selectors.Config
	limits    limitSettings
	devMode   bool
	servers   []*lxdServer // sorted by id
}

// pathData is what agent_path_template may use: configured and LXD-sourced
// values only (SEC-15).
type pathData struct {
	PluginName   string
	ServerID     string
	InstanceUUID string
}

// agentPath renders the agent's SPIFFE ID path, after the trust domain's
// /spire/agent/ (FR-S7).
func (s *settings) agentPath(serverID, instanceUUID string) (string, error) {
	var b strings.Builder
	if err := s.pathTemplate.Execute(&b, pathData{PluginName: pluginName, ServerID: serverID, InstanceUUID: instanceUUID}); err != nil {
		return "", fmt.Errorf("agent_path_template: %w", err)
	}
	if err := validPath(b.String()); err != nil {
		return "", fmt.Errorf("agent_path_template renders %w", err)
	}
	return b.String(), nil
}

// invalid wraps a configuration error as config.ErrInvalid.
func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", config.ErrInvalid, fmt.Sprintf(format, a...))
}

// newSettings validates c (Validation at Configure) and prepares a client
// per server, without contacting any (FR-S12 checks them on first use).
func newSettings(ctx context.Context, c *Config, trustDomain string) (*settings, error) {
	if trustDomain == "" {
		return nil, invalid("SPIRE passed no trust domain")
	}
	s := &settings{trustDomain: trustDomain, devMode: c.DevMode, reattest: true}
	var err error
	if s.mode, err = proofMode(c); err != nil {
		return nil, err
	}
	// the proof timeout is also the server nonce's lifetime (SEC-03)
	if s.proofTimeout, err = config.Duration("proof_timeout", c.ProofTimeout, 10*time.Second, time.Second, 2*time.Minute); err != nil {
		return nil, err
	}
	if s.proofDir, err = proofDir(c.ProofDir); err != nil {
		return nil, err
	}
	switch c.Reattest {
	case "", "allow":
	case "deny":
		s.reattest = false
	default:
		return nil, invalid("reattest must be allow or deny")
	}
	if s.pathTemplate, err = template.New("agent_path_template").Option("missingkey=error").Parse(cmpOr(c.AgentPathTemplate, defaultPathTemplate)); err != nil {
		return nil, invalid("agent_path_template: %v", err)
	}
	if s.policy, err = policySettings(c.Policy, s.proofDir); err != nil {
		return nil, err
	}
	if c.Selectors != nil {
		s.selectors = selectors.Config{Enable: c.Selectors.Enable, ConfigKeys: c.Selectors.ConfigKeys, UserKeys: c.Selectors.UserKeys}
	}
	if err := s.selectors.Validate(); err != nil {
		return nil, invalid("selectors: %v", err)
	}
	if s.limits, err = limitsSettings(c.Limits); err != nil {
		return nil, err
	}
	if s.servers, err = serverSettings(ctx, c, s); err != nil {
		return nil, err
	}
	if c.DevMode {
		slog.Warn("dev_mode is on: LXD servers without a pin or CA bundle are not verified (SEC-07)")
	}
	return s, nil
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// proofMode checks proof_mode and the acknowledgement of none (SEC-02).
func proofMode(c *Config) (string, error) {
	mode := cmpOr(c.ProofMode, modeFilePull)
	switch mode {
	case modeFilePull, modeAuto:
	case modeConfigPush:
		return "", invalid(`proof_mode "config_push" is not implemented in this version: use file_pull or auto`)
	case modeNone:
		if !c.ClaimOnlyAck {
			return "", invalid(`proof_mode "none" issues identities on a claim alone: it needs i_understand_claim_only_is_insecure = true (SEC-02)`)
		}
		slog.Warn(`proof_mode is "none": identities are issued on a claim alone, without proof of co-location (SEC-02)`)
	default:
		return "", invalid("proof_mode must be file_pull, config_push, auto or none")
	}
	return mode, nil
}

// proofDir checks proof_dir: an absolute, clean path below /, short enough
// that every proof path fits the protocol's string limit (SEC-11).
func proofDir(dir string) (string, error) {
	dir = cmpOr(dir, defaultProofDir)
	switch {
	case !filepath.IsAbs(dir) || filepath.Clean(dir) != dir:
		return "", invalid("proof_dir must be an absolute, clean path")
	case dir == "/":
		return "", invalid("proof_dir must not be /")
	case len(dir)+len("/proof-")+16 > limits.MaxString:
		return "", invalid("proof_dir is too long: proof paths must fit %d bytes", limits.MaxString)
	}
	return dir, nil
}

func policySettings(c *PolicyConfig, proofDir string) (policy.Policy, error) {
	p := policy.Policy{ProofDir: proofDir}
	if c == nil {
		return p, nil
	}
	if slices.Contains(c.DenyConfigKeys, "") {
		return p, invalid("policy.deny_config_keys: an empty key")
	}
	p.AllowPrivileged, p.DenyConfigKeys = c.AllowPrivileged, c.DenyConfigKeys
	return p, nil
}

func limitsSettings(c *LimitsConfig) (limitSettings, error) {
	l := limitSettings{maxInflight: 16, ratePerSec: 20}
	if c == nil {
		c = &LimitsConfig{}
	}
	bounded := func(key string, v, def, max int) (int, error) {
		switch {
		case v == 0:
			return def, nil
		case v < 0 || v > max:
			return 0, invalid("limits.%s must be between 1 and %d", key, max)
		}
		return v, nil
	}
	var err error
	if l.maxInflight, err = bounded("max_inflight", c.MaxInflight, 16, 1024); err != nil {
		return l, err
	}
	if l.ratePerSec, err = bounded("rate_per_sec", c.RatePerSec, 20, 10000); err != nil {
		return l, err
	}
	l.failureCooldown, err = config.Duration("limits.failure_cooldown", c.FailureCooldown, 5*time.Second, 100*time.Millisecond, 5*time.Minute)
	return l, err
}

// serverSettings checks the servers entries (FR-S11, FR-S19, SEC-07,
// SEC-08) and prepares their clients.
func serverSettings(ctx context.Context, c *Config, s *settings) ([]*lxdServer, error) {
	if len(c.Servers) == 0 {
		return nil, invalid("servers: at least one LXD server is needed (FR-S11)")
	}
	ids := make([]string, 0, len(c.Servers))
	for id := range c.Servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []*lxdServer
	for _, id := range ids {
		sc := c.Servers[id]
		if err := checkServer(id, sc, c.DevMode); err != nil {
			return nil, err
		}
		if _, err := s.agentPath(id, sampleUUID); err != nil {
			return nil, invalid("server %s: %v", id, err)
		}
		srv, err := newLXDServer(ctx, id, sc, c.DevMode)
		if err != nil {
			return nil, invalid("server %s: %v", id, err)
		}
		out = append(out, srv)
	}
	return out, nil
}

// checkServer checks one entry, its credential files included, before a
// client is made for it.
func checkServer(id string, sc ServerConfig, devMode bool) error {
	if err := validSegment(id); err != nil {
		return invalid("server id %q: %v (it is the server_id of the SPIFFE ID)", id, err)
	}
	if err := checkURL(sc.URL); err != nil {
		return invalid("server %s: url: %v", id, err)
	}
	if err := checkCredential(sc); err != nil {
		return invalid("server %s: %v", id, err)
	}
	switch {
	case sc.ServerCertFingerprint != "" && sc.CABundleFile != "":
		return invalid("server %s: server_cert_fingerprint or ca_bundle_file, not both (SEC-07)", id)
	case sc.ServerCertFingerprint == "" && sc.CABundleFile == "" && !devMode:
		return invalid("server %s: server_cert_fingerprint or ca_bundle_file is required (SEC-07)", id)
	}
	if len(sc.Projects) == 0 {
		return invalid("server %s: projects: at least one project", id)
	}
	for i, p := range sc.Projects {
		if err := claim.ValidProject(p); err != nil {
			return invalid("server %s: projects: %v", id, err)
		}
		if slices.Contains(sc.Projects[:i], p) {
			return invalid("server %s: projects: %q is listed twice", id, p)
		}
	}
	return nil
}

// checkURL accepts https://host[:port] and nothing else.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return err
	case u.Scheme != "https" || u.Host == "":
		return errors.New("must be https://host[:port]")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return errors.New("must have no user, path, query or fragment")
	}
	return nil
}

// checkCredential requires exactly one credential, from files only their
// owner may read (FR-S19, SEC-08).
func checkCredential(sc ServerConfig) error {
	cert, key, token := sc.ClientCertFile != "", sc.ClientKeyFile != "", sc.BearerTokenFile != ""
	switch {
	case token && (cert || key):
		return errors.New("a client certificate or bearer_token_file, not both (FR-S19)")
	case token:
		t, err := lxd.ReadSecretFile(sc.BearerTokenFile)
		if err != nil {
			return err
		}
		return lxd.CheckBearerToken(t)
	case cert != key:
		return errors.New("client_cert_file and client_key_file go together")
	case !cert:
		return errors.New("a credential is required: client_cert_file and client_key_file, or bearer_token_file")
	}
	if _, err := os.Stat(sc.ClientCertFile); err != nil {
		return fmt.Errorf("client_cert_file: %w", err)
	}
	info, err := os.Stat(sc.ClientKeyFile)
	if err != nil {
		return fmt.Errorf("client_key_file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("client_key_file is readable by group or others (mode %o, SEC-08)", info.Mode().Perm())
	}
	return nil
}

// validSegment accepts a SPIFFE ID path segment: letters, digits, ".",
// "-" and "_", but not "." or "..".
func validSegment(s string) error {
	switch {
	case s == "" || len(s) > limits.MaxName:
		return fmt.Errorf("must be 1 to %d characters", limits.MaxName)
	case s == "." || s == "..":
		return errors.New(`must not be "." or ".."`)
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '-' && c != '_' {
			return errors.New("may hold letters, digits, '.', '-' and '_' only")
		}
	}
	return nil
}

// validPath accepts a relative path of SPIFFE ID segments.
func validPath(p string) error {
	for seg := range strings.SplitSeq(p, "/") {
		if err := validSegment(seg); err != nil {
			return fmt.Errorf("an invalid SPIFFE ID path segment: %w", err)
		}
	}
	return nil
}

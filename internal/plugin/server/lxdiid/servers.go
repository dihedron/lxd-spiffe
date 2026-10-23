package lxdiid

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
)

// lxdServer is one entry of servers: its client, and whether that client
// passed the capability check of FR-S12.
type lxdServer struct {
	id       string
	projects []string
	// conn makes the client; for a bearer entry it holds the token read last.
	conn      lxd.Config
	tokenFile string

	mu         sync.Mutex
	client     *lxd.Client
	ready      bool
	tokenStamp fileStamp
	// lastErr is the last refusal logged, so that it is logged once.
	lastErr string

	breaker breaker
}

// fileStamp tells whether a file changed.
type fileStamp struct {
	mod  time.Time
	size int64
}

func newLXDServer(ctx context.Context, id string, sc ServerConfig, devMode bool, b breakerSettings) (*lxdServer, error) {
	s := &lxdServer{
		breaker:  breaker{breakerSettings: b, now: time.Now, wait: b.open},
		id:       id,
		projects: sc.Projects,
		conn: lxd.Config{
			Endpoint:       sc.URL,
			Pin:            sc.ServerCertFingerprint,
			CAFile:         sc.CABundleFile,
			ClientCertFile: sc.ClientCertFile,
			ClientKeyFile:  sc.ClientKeyFile,
			// GET /1.0 and the project reads go to the first project
			Project:  sc.Projects[0],
			Insecure: devMode,
		},
		tokenFile: sc.BearerTokenFile,
	}
	if s.tokenFile != "" {
		if err := s.readToken(); err != nil {
			return nil, err
		}
	}
	var err error
	s.client, err = lxd.Connect(ctx, s.conn)
	return s, err
}

// checked returns the server's client once it passed the capability check
// (FR-S12): GET /1.0 on first use and after every reconnect; a refused
// server fails closed, logged once. A bearer token is re-read when its
// file changes, and once after a 403 before failing (FR-S19).
func (s *lxdServer) checked(ctx context.Context) (*lxd.Client, error) {
	trial, err := s.breaker.allow()
	if err != nil {
		return nil, fmt.Errorf("server %s: %w", s.id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if trial {
		// the trial after an open breaker starts from a new connection
		if err := s.reconnect(ctx); err != nil {
			s.breaker.observe(err)
			return nil, s.refuse(err)
		}
	}
	if s.tokenFile != "" && s.tokenChanged() {
		if err := s.rotate(ctx); err != nil {
			return nil, s.refuse(err)
		}
	}
	if s.ready {
		return s.client, nil
	}
	err = s.check(ctx)
	if err != nil && s.tokenFile != "" && lxd.KindOf(err) == lxd.KindAuth {
		// issuing a new token revokes the old one at once
		if err = s.rotate(ctx); err == nil {
			err = s.check(ctx)
		}
	}
	if err != nil {
		return nil, s.refuse(err)
	}
	s.ready, s.lastErr = true, ""
	s.warnIdentity(ctx)
	return s.client, nil
}

// observe tells the breaker how an LXD call through this server went; a
// call cut short by the attestation's own deadline says nothing of LXD.
func (s *lxdServer) observe(ctx context.Context, err error) {
	if ctx.Err() == nil {
		s.breaker.observe(err)
	}
}

// warnIdentity warns, after a server's check, when the plugin's LXD identity
// appears to hold server administrator rights (SEC-16, gate L9); a server
// without the endpoint is skipped.
func (s *lxdServer) warnIdentity(ctx context.Context) {
	info, err := s.client.CurrentIdentity(ctx)
	if err != nil {
		return
	}
	for _, w := range identityWarnings(info) {
		slog.Warn("the LXD identity has more rights than the attestor needs (SEC-16)", "server_id", s.id, "finding", w)
	}
}

// identityWarnings lists what makes an identity look like a server
// administrator: not fine-grained (a legacy trusted certificate has every
// right) or an admin entitlement on the server.
func identityWarnings(info *api.IdentityInfo) []string {
	var out []string
	if !info.FineGrained {
		out = append(out, "the identity is not fine-grained: it has every right on the server")
	}
	for _, p := range info.EffectivePermissions {
		if p.EntityType == "server" && p.Entitlement == "admin" {
			out = append(out, "the identity has the server's admin entitlement")
		}
	}
	return out
}

// reconnect makes a new client, to be checked again.
func (s *lxdServer) reconnect(ctx context.Context) error {
	client, err := lxd.Connect(ctx, s.conn)
	if err != nil {
		return err
	}
	s.client, s.ready = client, false
	return nil
}

// check reads GET /1.0 and requires trust and the API extensions, with
// auth_bearer for a bearer entry.
func (s *lxdServer) check(ctx context.Context) error {
	srv, err := s.client.Server(ctx)
	// only the call tells of LXD's health; the verdict below is about trust
	s.breaker.observe(err)
	if err != nil {
		return err
	}
	extensions := lxd.RequiredExtensions
	if s.tokenFile != "" {
		extensions = append(slices.Clone(extensions), "auth_bearer")
	}
	return lxd.CheckServer(srv, extensions...)
}

// refuse logs a refusal the first time it happens in a row.
func (s *lxdServer) refuse(err error) error {
	if msg := err.Error(); msg != s.lastErr {
		slog.Error("refusing to attest through an LXD server", "server_id", s.id, "error", msg)
		s.lastErr = msg
	}
	return fmt.Errorf("server %s: %w", s.id, err)
}

// rotate re-reads the token and reconnects; the new client is checked again.
func (s *lxdServer) rotate(ctx context.Context) error {
	if err := s.readToken(); err != nil {
		return err
	}
	return s.reconnect(ctx)
}

// readToken reads the bearer token (SEC-08) and refuses an unsigned one.
func (s *lxdServer) readToken() error {
	stamp := statFile(s.tokenFile)
	token, err := lxd.ReadSecretFile(s.tokenFile)
	if err != nil {
		return err
	}
	if err := lxd.CheckBearerToken(token); err != nil {
		return err
	}
	s.conn.BearerToken, s.tokenStamp = token, stamp
	return nil
}

func (s *lxdServer) tokenChanged() bool {
	return statFile(s.tokenFile) != s.tokenStamp
}

func statFile(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{mod: info.ModTime(), size: info.Size()}
}

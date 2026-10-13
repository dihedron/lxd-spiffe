package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/canonical/lxd/shared/api"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/expect"
)

// BearerCheck is server bearer check (PRS-41): what the token claims, and
// whether and until when LXD accepts it.
type BearerCheck struct {
	probe.ServerCommand
	WaitExpiry bool          `long:"wait-expiry" description:"Keep calling until the token is refused and record the first refusal."`
	MaxWait    time.Duration `long:"max-wait" description:"Upper bound for --wait-expiry." default:"1h"`
	Interval   time.Duration `long:"interval" description:"Poll interval of --wait-expiry." default:"1s"`

	stdout io.Writer
}

// tokenClaims are the claims bearer check records: never the token, its
// signature, the subject or any other claim.
type tokenClaims struct {
	Issuer                       string   `json:"iss,omitempty"`
	Audience                     []string `json:"aud,omitempty"`
	IssuedAt                     string   `json:"iat,omitempty"`
	NotBefore                    string   `json:"nbf,omitempty"`
	Expires                      string   `json:"exp,omitempty"`
	LifetimeS                    *int64   `json:"lifetime_s,omitempty"`
	SubjectPresent               bool     `json:"sub_present"`
	ServerCertFingerprintPresent bool     `json:"server_cert_fingerprint_present"`
}

// pollResult is one call of --wait-expiry as recorded.
type pollResult struct {
	At             string   `json:"at"`
	Status         int      `json:"status,omitempty"`
	Error          string   `json:"error,omitempty"`
	OffsetFromExpS *float64 `json:"offset_from_exp_s,omitempty"`
}

// Execute runs the command.
func (cmd *BearerCheck) Execute(args []string) error {
	if cmd.Auth != "bearer" {
		return exit.New(exit.Usage, errors.New("server bearer check: needs --auth bearer"))
	}
	if cmd.WaitExpiry && (cmd.Interval <= 0 || cmd.MaxWait <= 0) {
		return exit.New(exit.Usage, errors.New("server bearer check: --interval and --max-wait must be positive"))
	}
	timeout := cmd.Timeout
	recorded := []string{}
	if cmd.WaitExpiry {
		timeout += cmd.MaxWait
		recorded = append(recorded, "--wait-expiry", "--max-wait", cmd.MaxWait.String(), "--interval", cmd.Interval.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s, err := newSession(ctx, &cmd.ServerCommand, cmd.stdout, "bearer check", recorded)
	if err != nil {
		return err
	}
	token, err := bearerToken(&cmd.ServerCommand)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("server bearer check: %w", err))
	}
	return s.finish(cmd.run(ctx, s, token))
}

func (cmd *BearerCheck) run(ctx context.Context, s *session, token string) error {
	exp := observeToken(s, token)
	server, err := s.readServer(ctx, &expect.Server)
	if err != nil {
		return err
	}
	if s.server != nil {
		s.observe("server.auth", s.server.Auth)
	}
	identity, err := s.call(ctx, "identity", lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/auth/identities/current"}, &expect.IdentityCurrent)
	if err != nil {
		return err
	}
	var id api.IdentityInfo
	if s.decode("identity", identity, &id) {
		s.observe("identity.authentication_method", id.AuthenticationMethod)
		s.observe("identity.type", id.Type)
		s.observe("identity.groups", id.Groups)
	}
	s.observe("bearer.calls", map[string]string{"server": verdict(server.Status), "identity": verdict(identity.Status)})
	if !cmd.WaitExpiry {
		return nil
	}
	if lxd.StatusKind(identity.Status) != lxd.KindNone {
		s.observe("expiry.skipped", "the token is refused already")
		return nil
	}
	return cmd.waitExpiry(ctx, s, exp)
}

// observeToken records the token's JWT header and claims, decoded without
// verification, and returns its expiry (zero when it has none). The SDK
// has parsed it already when connecting: a token that is not a JWT never
// gets here.
func observeToken(s *session, token string) time.Time {
	claims := jwt.MapClaims{}
	parsed, _, err := jwt.NewParser().ParseUnverified(token, claims)
	if err != nil {
		s.finding("token.format", "not a JWT: "+err.Error())
		return time.Time{}
	}
	s.observe("token.format", "jwt")
	header := map[string]any{}
	for _, k := range []string{"alg", "typ"} {
		if v, ok := parsed.Header[k]; ok {
			header[k] = v
		}
	}
	s.observe("token.header", header)
	out := tokenClaims{}
	out.Issuer, _ = claims.GetIssuer()
	out.Audience, _ = claims.GetAudience()
	utc := func(d *jwt.NumericDate) string {
		if d == nil {
			return ""
		}
		return d.UTC().Format(time.RFC3339)
	}
	iat, _ := claims.GetIssuedAt()
	nbf, _ := claims.GetNotBefore()
	exp, _ := claims.GetExpirationTime()
	out.IssuedAt, out.NotBefore, out.Expires = utc(iat), utc(nbf), utc(exp)
	if iat != nil && exp != nil {
		lifetime := exp.Unix() - iat.Unix()
		out.LifetimeS = &lifetime
	}
	_, out.SubjectPresent = claims["sub"]
	_, out.ServerCertFingerprintPresent = claims["server_cert_fingerprint"]
	s.observe("token.claims", out)
	if exp == nil {
		return time.Time{}
	}
	return exp.Time
}

// waitExpiry polls GET /1.0/auth/identities/current until the first
// refusal or --max-wait. The polls use the client directly, as instance
// watch does: one artifact each would drown the run.
func (cmd *BearerCheck) waitExpiry(ctx context.Context, s *session, exp time.Time) error {
	result := func(at time.Time, status int, msg string) pollResult {
		r := pollResult{At: at.UTC().Format(time.RFC3339Nano), Status: status, Error: msg}
		if !exp.IsZero() {
			offset := at.Sub(exp).Seconds()
			r.OffsetFromExpS = &offset
		}
		return r
	}
	start := time.Now()
	last := result(start, http.StatusOK, "")
	polls, failures := 0, 0
	for deadline := start.Add(cmd.MaxWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cmd.Interval):
		}
		resp, err := s.client.Raw(ctx, lxd.RawRequest{Method: http.MethodGet, Path: "/1.0/auth/identities/current"})
		at := time.Now()
		polls++
		if err != nil {
			failures++
			continue
		}
		if lxd.StatusKind(resp.Status) == lxd.KindNone {
			last = result(at, resp.Status, "")
			continue
		}
		msg := ""
		if env, err := resp.Envelope(); err == nil {
			msg = env.Error
		}
		if _, err := s.entry.ArtifactResponse("refusal", resp.Status, resp.Header, resp.Body); err != nil {
			return err
		}
		s.observe("expiry.polls", polls)
		s.observe("expiry.last_success", last)
		s.observe("expiry.first_refusal", result(at, resp.Status, msg))
		if failures > 0 {
			s.observe("expiry.poll_failures", failures)
		}
		return nil
	}
	s.observe("expiry.polls", polls)
	s.observe("expiry.last_success", last)
	if failures > 0 {
		s.observe("expiry.poll_failures", failures)
	}
	s.finding("expiry.not_reached", fmt.Sprintf("the token was still accepted after --max-wait %s", cmd.MaxWait))
	return nil
}

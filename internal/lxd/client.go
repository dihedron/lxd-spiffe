// Package lxd is the LXD client of the plugins and of lxd-probe (attestor
// spec FR-S17): a thin wrapper around the LXD Go client SDK that adds the
// project's rules. TLS 1.3 with the server certificate pinned by
// fingerprint, the client certificate reloaded when its files change,
// redirects refused, response bodies limited, the project always explicit,
// and every exchange available to a recorder. Callers never use the SDK
// connection directly, except through SDK().
package lxd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	lxdclient "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/pkg/metadata"
)

// DefaultMaxBytes is the default limit of a response body (FR-S17 (6)).
const DefaultMaxBytes = 1 << 20

// Config configures a connection.
type Config struct {
	// Endpoint is https://host:port or unix:///path/to/unix.socket.
	Endpoint string
	// Pin is the SHA-256 fingerprint of the server certificate, in hex,
	// optionally prefixed by "sha256:" or separated by colons. Mandatory
	// for https.
	Pin string
	// ClientCertFile and ClientKeyFile hold the TLS identity; both or none.
	ClientCertFile string
	ClientKeyFile  string
	// BearerToken authenticates with a bearer token instead of, or besides,
	// the client certificate. The SDK requires it to be a JWT, as LXD bearer
	// tokens are; a server fingerprint claim in it is checked too.
	BearerToken string
	// Project is sent with every request.
	Project string
	// UserAgent defaults to lxd-spiffe/<version>.
	UserAgent string
	// MaxBytes limits response bodies; DefaultMaxBytes when zero.
	MaxBytes int64
	// Recorder, when set, receives every exchange, unsanitized.
	Recorder func(Exchange)
}

// Exchange is one request and its response, as seen on the wire.
type Exchange struct {
	Method         string
	Path           string
	Query          string
	RequestHeader  http.Header
	RequestBody    []byte
	Status         int
	ResponseHeader http.Header
	ResponseBody   []byte
}

// Client is a connection to one LXD server.
type Client struct {
	sdk     lxdclient.InstanceServer
	base    *url.URL
	project string
}

// Connect validates cfg and prepares a connection. No request is sent: the
// SDK's initial GET /1.0 is skipped, so that callers decide what to call.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Project == "" {
		return nil, errors.New("lxd: project is required")
	}
	if (cfg.ClientCertFile == "") != (cfg.ClientKeyFile == "") {
		return nil, errors.New("lxd: client certificate and key go together")
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "lxd-spiffe/" + metadata.Version
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("lxd: endpoint: %w", err)
	}
	args := &lxdclient.ConnectionArgs{
		UserAgent:     cfg.UserAgent,
		BearerToken:   cfg.BearerToken,
		SkipGetServer: true,
	}
	var sdk lxdclient.InstanceServer
	var base *url.URL
	switch endpoint.Scheme {
	case "https":
		pin, err := normalizePin(cfg.Pin)
		if err != nil {
			return nil, err
		}
		var certs *certLoader
		if cfg.ClientCertFile != "" {
			certs = &certLoader{certFile: cfg.ClientCertFile, keyFile: cfg.ClientKeyFile}
		}
		args.TransportWrapper = func(t *http.Transport) lxdclient.HTTPTransporter {
			// pinned mode: chain verification is replaced by the pin
			t.TLSClientConfig.InsecureSkipVerify = true
			t.TLSClientConfig.VerifyConnection = pinVerifier(pin)
			t.TLSClientConfig.MinVersion = tls.VersionTLS13
			if certs != nil {
				t.TLSClientConfig.Certificates = nil
				t.TLSClientConfig.GetClientCertificate = certs.get
			}
			return &transport{base: t, maxBytes: cfg.MaxBytes, recorder: cfg.Recorder}
		}
		sdk, err = lxdclient.ConnectLXDWithContext(ctx, endpoint.Scheme+"://"+endpoint.Host, args)
		if err != nil {
			return nil, &Error{Kind: KindConnection, Err: err}
		}
		base = &url.URL{Scheme: "https", Host: endpoint.Host}
	case "unix":
		args.TransportWrapper = func(t *http.Transport) lxdclient.HTTPTransporter {
			return &transport{base: t, maxBytes: cfg.MaxBytes, recorder: cfg.Recorder}
		}
		sdk, err = lxdclient.ConnectLXDUnixWithContext(ctx, endpoint.Path, args)
		if err != nil {
			return nil, &Error{Kind: KindConnection, Err: err}
		}
		base = &url.URL{Scheme: "http", Host: "unix.socket"}
	default:
		return nil, fmt.Errorf("lxd: endpoint %q: scheme must be https or unix", cfg.Endpoint)
	}
	return &Client{sdk: sdk.UseProject(cfg.Project), base: base, project: cfg.Project}, nil
}

// normalizePin returns a SHA-256 fingerprint as 64 lowercase hex digits.
func normalizePin(pin string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(pin))
	p = strings.TrimPrefix(p, "sha256:")
	p = strings.ReplaceAll(p, ":", "")
	if b, err := hex.DecodeString(p); err != nil || len(b) != 32 {
		return "", errors.New("lxd: pin must be a SHA-256 fingerprint (64 hex digits)")
	}
	return p, nil
}

// SDK returns the SDK connection, already scoped to the project, for typed
// calls. Its requests go through the same transport.
func (c *Client) SDK() lxdclient.InstanceServer {
	return c.sdk
}

// RawRequest is a request sent as is.
type RawRequest struct {
	Method string
	// Path is the API path, e.g. /1.0/instances/c1.
	Path string
	// Query is added to the project parameter.
	Query url.Values
	// Body is a JSON body, if any.
	Body []byte
	// ETag, if set, is sent as If-Match (FR-S17 (8)).
	ETag string
	// MaxBytes overrides the body limit for this request.
	MaxBytes int64
}

// RawResponse is a response as received, with the TLS parameters of its
// connection.
type RawResponse struct {
	Status      int
	Header      http.Header
	Body        []byte
	TLSVersion  uint16
	CipherSuite uint16
}

// Raw sends a request through the SDK's DoHTTP and returns the response of
// any status. Errors are transport errors only: connection, TLS, timeout,
// redirect or body over the limit.
func (c *Client) Raw(ctx context.Context, r RawRequest) (*RawResponse, error) {
	query := url.Values{}
	for k, v := range r.Query {
		query[k] = v
	}
	query.Set("project", c.project)
	u := *c.base
	u.Path = r.Path
	u.RawQuery = query.Encode()

	var body *bytes.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(withLimit(ctx, r.MaxBytes), r.Method, u.String(), body)
	} else {
		req, err = http.NewRequestWithContext(withLimit(ctx, r.MaxBytes), r.Method, u.String(), nil)
	}
	if err != nil {
		return nil, fmt.Errorf("lxd: building request: %w", err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.ETag != "" {
		req.Header.Set("If-Match", r.ETag)
	}
	resp, err := c.sdk.DoHTTP(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Kind: KindTimeout, Err: err}
		}
		return nil, wrapTransportError(err)
	}
	defer resp.Body.Close()
	data := new(bytes.Buffer)
	if _, err := data.ReadFrom(resp.Body); err != nil {
		return nil, wrapTransportError(err)
	}
	out := &RawResponse{Status: resp.StatusCode, Header: resp.Header, Body: data.Bytes()}
	if resp.TLS != nil {
		out.TLSVersion, out.CipherSuite = resp.TLS.Version, resp.TLS.CipherSuite
	}
	return out, nil
}

// Envelope decodes the LXD response envelope of the body.
func (r *RawResponse) Envelope() (*api.Response, error) {
	var env api.Response
	if err := json.Unmarshal(r.Body, &env); err != nil {
		return nil, &Error{Kind: KindProtocol, Status: r.Status, Err: fmt.Errorf("decoding envelope: %w", err)}
	}
	if env.Type != api.SyncResponse && env.Type != api.AsyncResponse && env.Type != api.ErrorResponse {
		return nil, &Error{Kind: KindProtocol, Status: r.Status, Err: fmt.Errorf("unknown envelope type %q", env.Type)}
	}
	return &env, nil
}

// Decode decodes the metadata of a sync response into v. An error envelope,
// or an error status, returns an *Error of the status's kind.
func Decode(r *RawResponse, v any) error {
	env, err := r.Envelope()
	if err != nil {
		return err
	}
	if env.Type == api.ErrorResponse || StatusKind(r.Status) != KindNone {
		status := env.Code
		if status == 0 {
			status = r.Status
		}
		return &Error{Kind: StatusKind(status), Status: status, Err: errors.New(env.Error)}
	}
	if err := json.Unmarshal(env.Metadata, v); err != nil {
		return &Error{Kind: KindProtocol, Status: r.Status, Err: fmt.Errorf("decoding metadata: %w", err)}
	}
	return nil
}

// ReadSecretFile reads a token from a file that only its owner can read
// (attestor spec SEC-08, probe spec PRN-12), without surrounding space.
func ReadSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file %s is readable by group or others (mode %o)", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

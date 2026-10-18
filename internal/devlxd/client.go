// Package devlxd is the devLXD client of the agent plugin and of lxd-probe
// (attestor spec FR-A3, probe spec PRF-14): a thin wrapper around the LXD
// Go client SDK's devLXD connection that sends requests as they are, through
// the SDK's DoHTTP, and returns responses as received. devLXD answers in its
// own plain format on the Unix socket (a JSON or text body, text errors)
// and in the LXD envelope over vsock; the client records which. Requests are
// limited in time and in body size, and every exchange is available to a
// recorder. Errors reuse the kinds of internal/lxd.
package devlxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	lxdclient "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/pkg/metadata"
)

// Defaults of the agent (FR-A3).
const (
	DefaultSocket   = "/dev/lxd/sock"
	DefaultTimeout  = 5 * time.Second
	DefaultMaxBytes = limits.MaxDevLXDResponse
)

// Config configures a connection.
type Config struct {
	// Socket is the devLXD socket; DefaultSocket when empty.
	Socket string
	// Timeout bounds each request; DefaultTimeout when zero.
	Timeout time.Duration
	// MaxBytes limits response bodies; DefaultMaxBytes when zero.
	MaxBytes int64
	// UserAgent defaults to lxd-spiffe/<version>.
	UserAgent string
	// Recorder, when set, receives every exchange, unsanitized.
	Recorder func(lxd.Exchange)
}

// Client is a connection to the devLXD socket.
type Client struct {
	sdk     lxdclient.DevLXDServer
	timeout time.Duration
}

// Connect checks the socket and prepares a connection; no request is sent.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "lxd-spiffe/" + metadata.Version
	}
	args := &lxdclient.ConnectionArgs{
		UserAgent: cfg.UserAgent,
		TransportWrapper: func(t *http.Transport) lxdclient.HTTPTransporter {
			return &transport{base: t, maxBytes: cfg.MaxBytes, recorder: cfg.Recorder}
		},
	}
	sdk, err := lxdclient.ConnectDevLXDWithContext(ctx, cfg.Socket, args)
	if err != nil {
		return nil, &lxd.Error{Kind: lxd.KindConnection, Err: err}
	}
	return &Client{sdk: sdk, timeout: cfg.Timeout}, nil
}

// Format is the format of a devLXD response body.
type Format string

// Response formats.
const (
	FormatPlain    Format = "plain"    // devLXD's own: the content itself
	FormatEnvelope Format = "envelope" // the LXD envelope, content in metadata
)

// Response is a devLXD response as received.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Format  Format
	Latency time.Duration
}

// Get sends GET path and returns the response of any status. Errors are
// transport errors only: no socket, timeout, or a body over the limit.
func (c *Client) Get(ctx context.Context, path string) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://lxd"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("devlxd: building request: %w", err)
	}
	start := time.Now()
	resp, err := c.sdk.DoHTTP(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read in full by the transport
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	return &Response{
		Status: resp.StatusCode, Header: resp.Header, Body: body,
		Format: DetectFormat(body), Latency: time.Since(start),
	}, nil
}

// Decode decodes the content of a successful response into v: the body of
// a plain response, the metadata of an envelope. An error status returns an
// *lxd.Error of the status's kind.
func (r *Response) Decode(v any) error {
	content := r.Body
	if r.Format == FormatEnvelope {
		var env api.Response
		if err := json.Unmarshal(r.Body, &env); err != nil {
			return &lxd.Error{Kind: lxd.KindProtocol, Status: r.Status, Err: err}
		}
		if env.Type == api.ErrorResponse {
			status := env.Code
			if status == 0 {
				status = r.Status
			}
			return &lxd.Error{Kind: lxd.StatusKind(status), Status: status, Err: errors.New(env.Error)}
		}
		content = env.Metadata
	}
	if kind := lxd.StatusKind(r.Status); kind != lxd.KindNone {
		return &lxd.Error{Kind: kind, Status: r.Status, Err: errors.New(string(bytes.TrimSpace(r.Body)))}
	}
	if err := json.Unmarshal(content, v); err != nil {
		return &lxd.Error{Kind: lxd.KindProtocol, Status: r.Status, Err: fmt.Errorf("decoding: %w", err)}
	}
	return nil
}

// DetectFormat tells an LXD envelope from devLXD's plain format: an
// envelope is a JSON object with a "type" of sync, async or error and a
// "metadata" or "status_code" field.
func DetectFormat(body []byte) Format {
	var env map[string]json.RawMessage
	if json.Unmarshal(body, &env) != nil {
		return FormatPlain
	}
	var typ string
	if json.Unmarshal(env["type"], &typ) != nil {
		return FormatPlain
	}
	_, metadata := env["metadata"]
	_, code := env["status_code"]
	switch api.ResponseType(typ) {
	case api.SyncResponse, api.AsyncResponse, api.ErrorResponse:
		if metadata || code {
			return FormatEnvelope
		}
	}
	return FormatPlain
}

// transportError classifies a transport error, a deadline included.
func transportError(ctx context.Context, err error) error {
	var e *lxd.Error
	if errors.As(err, &e) {
		return err
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return &lxd.Error{Kind: lxd.KindTimeout, Err: err}
	}
	return &lxd.Error{Kind: lxd.KindOf(err), Err: err}
}

// transport wraps the SDK's Unix transport: it limits and buffers response
// bodies and passes every exchange to the recorder.
type transport struct {
	base     *http.Transport
	maxBytes int64
	recorder func(lxd.Exchange)
}

// Transport implements the SDK's HTTPTransporter.
func (t *transport) Transport() *http.Transport {
	return t.base
}

// RoundTrip implements http.RoundTripper.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, t.maxBytes+1))
	resp.Body.Close() //nolint:errcheck // read: the body is replaced below
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > t.maxBytes {
		return nil, &lxd.Error{Kind: lxd.KindTooLarge, Status: resp.StatusCode, Err: fmt.Errorf("response body over %d bytes", t.maxBytes)}
	}
	if t.recorder != nil {
		t.recorder(lxd.Exchange{
			Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
			RequestHeader: req.Header.Clone(),
			Status:        resp.StatusCode, ResponseHeader: resp.Header.Clone(), ResponseBody: body,
		})
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

// Events subscribes to devLXD's event stream (through the SDK's
// websocket) and calls handler for each event, from another goroutine,
// until stop is called. An error means the stream is not available.
func (c *Client) Events(handler func(api.Event)) (stop func(), err error) {
	listener, err := c.sdk.GetEvents()
	if err != nil {
		return nil, &lxd.Error{Kind: lxd.KindOf(err), Err: err}
	}
	if _, err := listener.AddHandler(nil, handler); err != nil {
		listener.Disconnect()
		return nil, &lxd.Error{Kind: lxd.KindOther, Err: err}
	}
	return listener.Disconnect, nil
}

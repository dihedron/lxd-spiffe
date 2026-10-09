package lxd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// limitKey carries a per-request body limit in the request context.
type limitKey struct{}

// transport wraps the SDK's *http.Transport (FR-S17): it refuses
// redirects, limits and buffers response bodies, and passes every exchange
// to the recorder. TLS settings are installed on the wrapped transport by
// wrapTransport.
type transport struct {
	base     *http.Transport
	maxBytes int64
	recorder func(Exchange)
}

// Transport implements the SDK's HTTPTransporter.
func (t *transport) Transport() *http.Transport {
	return t.base
}

// RoundTrip implements http.RoundTripper.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil && req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			reqBody, _ = io.ReadAll(body)
			body.Close()
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, wrapTransportError(err)
	}
	limit := t.maxBytes
	if l, ok := req.Context().Value(limitKey{}).(int64); ok && l > 0 {
		limit = l
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	resp.Body.Close()
	if err != nil {
		return nil, wrapTransportError(err)
	}
	if int64(len(body)) > limit {
		return nil, &Error{Kind: KindTooLarge, Status: resp.StatusCode, Err: fmt.Errorf("response body over %d bytes", limit)}
	}
	if t.recorder != nil {
		t.recorder(Exchange{
			Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
			RequestHeader: req.Header.Clone(), RequestBody: reqBody,
			Status: resp.StatusCode, ResponseHeader: resp.Header.Clone(), ResponseBody: body,
		})
	}
	if StatusKind(resp.StatusCode) == KindRedirect {
		return nil, &Error{Kind: KindRedirect, Status: resp.StatusCode, Err: fmt.Errorf("redirect to %q refused", resp.Header.Get("Location"))}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

func wrapTransportError(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return &Error{Kind: classify(err), Err: err}
}

// withLimit returns a context carrying a body limit for one request.
func withLimit(ctx context.Context, limit int64) context.Context {
	if limit <= 0 {
		return ctx
	}
	return context.WithValue(ctx, limitKey{}, limit)
}

// pinVerifier returns a VerifyConnection function that accepts only a leaf
// certificate with the given SHA-256 fingerprint (FR-S17 (1)).
func pinVerifier(pin string) func(tls.ConnectionState) error {
	want := []byte(pin)
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return &Error{Kind: KindConnection, Err: errors.New("server presented no certificate")}
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		got := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			return &Error{Kind: KindConnection, Err: fmt.Errorf("server certificate fingerprint %s does not match the pin", got)}
		}
		return nil
	}
}

// certLoader loads the client certificate from its files and reloads it
// when either file changes (FR-S17 (1)).
type certLoader struct {
	certFile, keyFile string

	mu      sync.Mutex
	stamp   string
	current *tls.Certificate
}

func (l *certLoader) get(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stamp, err := fileStamp(l.certFile, l.keyFile)
	if err != nil {
		return nil, err
	}
	if l.current != nil && stamp == l.stamp {
		return l.current, nil
	}
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading client certificate: %w", err)
	}
	l.current, l.stamp = &cert, stamp
	return l.current, nil
}

// fileStamp identifies the current version of files by size and time.
func fileStamp(paths ...string) (string, error) {
	var b bytes.Buffer
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return "", fmt.Errorf("client certificate: %w", err)
		}
		fmt.Fprintf(&b, "%s:%d:%s;", p, info.Size(), info.ModTime().Format(time.RFC3339Nano))
	}
	return b.String(), nil
}

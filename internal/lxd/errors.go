package lxd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// Kind classifies an error or an HTTP status (FR-S17 (4)).
type Kind string

// Error kinds.
const (
	KindNone         Kind = ""
	KindConnection   Kind = "connection"   // dial, TLS or pin failure
	KindAuth         Kind = "auth"         // 401, 403
	KindNotFound     Kind = "not-found"    // 404
	KindPrecondition Kind = "precondition" // 412
	KindTimeout      Kind = "timeout"      // deadline exceeded
	KindRedirect     Kind = "redirect"     // 3xx, never followed
	KindTooLarge     Kind = "too-large"    // body over the limit
	KindServer       Kind = "server"       // other 4xx and 5xx
	KindProtocol     Kind = "protocol"     // not a valid LXD response
	KindOther        Kind = "other"
)

// Error is an error of the LXD client with its kind.
type Error struct {
	Kind   Kind
	Status int // HTTP status, when the error comes from a response
	Err    error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("lxd: %s (HTTP %d): %v", e.Kind, e.Status, e.Err)
	}
	return fmt.Sprintf("lxd: %s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// KindOf returns the kind of err: the kind of the first *Error in its
// chain, or a kind inferred from network and context errors.
func KindOf(err error) Kind {
	if err == nil {
		return KindNone
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return classify(err)
}

// StatusKind returns the kind of an HTTP status; KindNone for 1xx and 2xx.
func StatusKind(status int) Kind {
	switch {
	case status < 300:
		return KindNone
	case status < 400:
		return KindRedirect
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return KindAuth
	case status == http.StatusNotFound:
		return KindNotFound
	case status == http.StatusPreconditionFailed:
		return KindPrecondition
	}
	return KindServer
}

// classify infers the kind of a transport error.
func classify(err error) Kind {
	var (
		netErr  net.Error
		opErr   *net.OpError
		certErr *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		alert   tls.AlertError
		recErr  tls.RecordHeaderError
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	case errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout
	case errors.As(err, &certErr), errors.As(err, &unknown), errors.As(err, &alert), errors.As(err, &recErr):
		return KindConnection
	case errors.As(err, &opErr):
		return KindConnection
	}
	return KindOther
}

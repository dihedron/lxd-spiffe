package lxd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
)

// Handshake is the result of a bare TLS handshake (probe spec PRS-03).
type Handshake struct {
	// Accepted is true when the handshake completed.
	Accepted bool
	// Version and CipherSuite are those negotiated, when accepted.
	Version     uint16
	CipherSuite uint16
	// PinMatch is true when the presented leaf certificate has the pinned
	// fingerprint; only meaningful when accepted.
	PinMatch bool
	// Err is why the server refused the handshake, when not accepted.
	Err error
}

// ProbeTLS performs a TLS handshake limited to versions min..max against an
// https endpoint and closes the connection: no HTTP request is sent and no
// client certificate is presented. A refused handshake is a result, not an
// error; an error means the endpoint could not be reached. The pin is
// checked after the handshake and reported, so that a mismatch does not
// hide whether the server accepts the version.
func ProbeTLS(ctx context.Context, endpoint, pin string, min, max uint16) (*Handshake, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("lxd: endpoint: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("lxd: endpoint %q: a TLS handshake needs https", endpoint)
	}
	pin, err = normalizePin(pin)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "8443")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, &Error{Kind: classify(err), Err: err}
	}
	defer conn.Close()
	tc := tls.Client(conn, &tls.Config{
		ServerName: u.Hostname(),
		MinVersion: min,
		MaxVersion: max,
		// the pin is compared below, after the handshake
		InsecureSkipVerify: true, //nolint:gosec // pinned: see PinMatch
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Kind: KindTimeout, Err: err}
		}
		return &Handshake{Err: err}, nil
	}
	state := tc.ConnectionState()
	h := &Handshake{Accepted: true, Version: state.Version, CipherSuite: state.CipherSuite}
	if len(state.PeerCertificates) > 0 {
		sum := sha256.Sum256(state.PeerCertificates[0].Raw)
		h.PinMatch = subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(pin)) == 1
	}
	return h, nil
}

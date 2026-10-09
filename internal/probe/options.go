// Package probe holds what the lxd-probe commands share: the base option
// structs that leaf commands embed (spec PRN-31, PRN-32) and small option
// types. The commands themselves live in the guest, server and util
// subpackages.
package probe

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// GlobalOptions are the options of every command (spec: Global flags).
// It has no Execute method: leaf commands embed it.
type GlobalOptions struct {
	// EvidenceDir is the root of the evidence tree.
	EvidenceDir string `long:"evidence-dir" description:"Root of the evidence tree." default:"./evidence" env:"LXD_PROBE_EVIDENCE_DIR"`
	// RunID identifies the run; empty means a new one is generated.
	RunID string `long:"run-id" description:"Run identifier (default: UTC timestamp and 4 random hex digits)." env:"LXD_PROBE_RUN_ID"`
	// Gate tags every record with the validation gate it supports.
	Gate string `long:"gate" description:"Validation gate the records support (e.g. L14)." env:"LXD_PROBE_GATE"`
	// Cell tags every record with the lab-matrix cell.
	Cell string `long:"cell" description:"Lab-matrix cell of the records (e.g. lxd6-unpriv-ubuntu)." env:"LXD_PROBE_CELL"`
	// JSON prints the result as JSON on stdout.
	JSON bool `long:"json" description:"Print the result as JSON on stdout."`
	// Timeout bounds the command.
	Timeout time.Duration `long:"timeout" description:"Per-command timeout." default:"30s"`
	// Record stores sanitized request/response pairs as fixture candidates.
	Record string `long:"record" description:"Also store sanitized request/response pairs in this directory as fixture candidates."`
	// Strict turns unexpected outcomes into exit code 8.
	Strict bool `long:"strict" description:"Exit 8 when a record has an unexpected outcome."`
	// RedactNames replaces instance and project names with aliases.
	RedactNames bool `long:"redact-names" description:"Replace instance and project names with aliases."`
	// AllowWrite is required by every write verb (PRN-01); never from the environment.
	AllowWrite bool `long:"allow-write" description:"Allow this command to modify the server or the guest (requires --lab)."`
	// Lab acknowledges a lab system; never from the environment.
	Lab bool `long:"lab" description:"Acknowledge that the target is a lab system (requires --allow-write)."`
	// DryRun makes write verbs print what they would do and exit.
	DryRun bool `long:"dry-run" description:"Print what a write would do and exit without acting."`
}

// GuestCommand holds the options of the guest environment (spec:
// Guest-environment flags). It has no Execute method: guest leaf commands
// embed it.
type GuestCommand struct {
	GlobalOptions
	// Socket is the devLXD socket.
	Socket string `long:"socket" description:"Path of the devLXD socket." default:"/dev/lxd/sock"`
	// AllowSpirePaths also allows writes under /run/spire/lxd/ (PRN-02).
	AllowSpirePaths bool `long:"allow-spire-paths" description:"Also allow writes under /run/spire/lxd/ to rehearse the real proof path."`
}

// ServerCommand holds the options of the server environment (spec:
// Server-environment flags). It has no Execute method: server leaf commands
// embed it.
type ServerCommand struct {
	GlobalOptions
	// Endpoint is the LXD API endpoint.
	Endpoint string `long:"endpoint" description:"LXD endpoint: https://host:8443 or unix:///path/to/unix.socket." env:"LXD_PROBE_ENDPOINT"`
	// Pin is the SHA-256 fingerprint of the server certificate.
	Pin string `long:"pin" description:"SHA-256 fingerprint of the server certificate (mandatory for https)." env:"LXD_PROBE_PIN"`
	// ClientCert is the client certificate file.
	ClientCert string `long:"client-cert" description:"Client certificate file (TLS identity)." env:"LXD_PROBE_CLIENT_CERT"`
	// ClientKey is the client key file.
	ClientKey string `long:"client-key" description:"Client key file (TLS identity)." env:"LXD_PROBE_CLIENT_KEY"`
	// Auth is the authentication mode.
	Auth string `long:"auth" description:"Authentication mode." choice:"tls" choice:"bearer" default:"tls" env:"LXD_PROBE_AUTH"`
	// TokenFile holds the bearer token; LXD_PROBE_TOKEN is the alternative.
	TokenFile string `long:"token-file" description:"File holding the bearer token (or set LXD_PROBE_TOKEN)." env:"LXD_PROBE_TOKEN_FILE"`
	// Project is the LXD project, always sent explicitly.
	Project string `long:"project" description:"LXD project." default:"default" env:"LXD_PROBE_PROJECT"`
	// AllowTLS12 accepts TLS 1.2 and records it as a finding (PRS-03).
	AllowTLS12 bool `long:"allow-tls12" description:"Accept TLS 1.2 and record it as a finding."`
	// AllowSpireKeys also allows writes to user.spire.challenge.* (PRN-02).
	AllowSpireKeys bool `long:"allow-spire-keys" description:"Also allow writes to user.spire.challenge.* to rehearse the real key layout."`
	// ExpectDenied makes 403 the expected result (PRE-06).
	ExpectDenied bool `long:"expect-denied" description:"Expect 403: record it as an observation and exit 0."`
}

// UIDList is a comma-separated list of uids, as in --as-uid 0,1000,65534.
type UIDList []uint32

// UnmarshalFlag implements flags.Unmarshaler.
func (l *UIDList) UnmarshalFlag(value string) error {
	if value == "" {
		return errors.New("empty uid list")
	}
	var uids UIDList
	for field := range strings.SplitSeq(value, ",") {
		uid, err := strconv.ParseUint(strings.TrimSpace(field), 10, 32)
		if err != nil {
			return fmt.Errorf("invalid uid %q: %w", field, err)
		}
		uids = append(uids, uint32(uid))
	}
	*l = uids
	return nil
}

// NotImplemented is returned by the commands of later chunks until they are
// implemented.
func NotImplemented(command string) error {
	return exit.New(exit.Internal, fmt.Errorf("%s: not implemented yet", command))
}

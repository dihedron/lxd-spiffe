// Package proof computes and compares the co-location proofs of attestor
// spec Trust model → Proof of co-location: nonces (SEC-03), the names
// derived from the agent nonce (SEC-04), the file_pull proof and the
// constant-time comparison. It is shared by the agent, the server and
// lxd-probe's rehearsals (PRS-52).
package proof

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/limits"
)

const (
	// NonceSize is the size of both nonces (SEC-03).
	NonceSize = limits.NonceSize
	// MaxNameLen bounds the project and instance names that enter a proof.
	MaxNameLen = limits.MaxName
	// KeyPrefix is the devLXD config key of config_push, before the id.
	KeyPrefix = "user.spire.challenge."
	// FilePrefix is the proof file name of file_pull, before the id.
	FilePrefix = "proof-"
	// idLen is the length of the id derived from the agent nonce.
	idLen = 16
	// domain separates this proof from any other use of SHA-256.
	domain = "spire-lxd-v1\x00"
)

// ErrInvalidInput is returned for an input outside the bounds; the input is
// never truncated or padded.
var ErrInvalidInput = errors.New("invalid proof input")

// NewNonce returns NonceSize bytes from crypto/rand (SEC-03, FR-A4).
func NewNonce() ([]byte, error) {
	b := make([]byte, NonceSize)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("reading a nonce: %w", err)
	}
	return b, nil
}

// ID returns hex(SHA-256(agentNonce))[:16], the only variable part of the
// proof path and key (SEC-04).
func ID(agentNonce []byte) (string, error) {
	if len(agentNonce) != NonceSize {
		return "", fmt.Errorf("%w: agent nonce of %d bytes", ErrInvalidInput, len(agentNonce))
	}
	sum := sha256.Sum256(agentNonce)
	return hex.EncodeToString(sum[:])[:idLen], nil
}

// Path returns the file_pull proof path <proofDir>/proof-<id>. proofDir must
// be absolute, clean and not the root.
func Path(proofDir string, agentNonce []byte) (string, error) {
	if !path.IsAbs(proofDir) || path.Clean(proofDir) != proofDir || proofDir == "/" {
		return "", fmt.Errorf("%w: proof directory %q", ErrInvalidInput, proofDir)
	}
	id, err := ID(agentNonce)
	if err != nil {
		return "", err
	}
	return path.Join(proofDir, FilePrefix+id), nil
}

// Key returns the config_push key user.spire.challenge.<id>.
func Key(agentNonce []byte) (string, error) {
	id, err := ID(agentNonce)
	if err != nil {
		return "", err
	}
	return KeyPrefix + id, nil
}

// Compute returns SHA-256("spire-lxd-v1" 0x00 ‖ u16be(len(project)) ‖ project
// ‖ u16be(len(instance)) ‖ instance ‖ serverNonce ‖ agentNonce), the
// file_pull proof. project may be empty; instance may not.
func Compute(project, instance string, serverNonce, agentNonce []byte) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	switch {
	case len(project) > MaxNameLen:
		return zero, fmt.Errorf("%w: project of %d bytes", ErrInvalidInput, len(project))
	case instance == "" || len(instance) > MaxNameLen:
		return zero, fmt.Errorf("%w: instance name of %d bytes", ErrInvalidInput, len(instance))
	case len(serverNonce) != NonceSize:
		return zero, fmt.Errorf("%w: server nonce of %d bytes", ErrInvalidInput, len(serverNonce))
	case len(agentNonce) != NonceSize:
		return zero, fmt.Errorf("%w: agent nonce of %d bytes", ErrInvalidInput, len(agentNonce))
	}
	h := sha256.New()
	h.Write([]byte(domain))
	for _, s := range []string{project, instance} {
		h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(s)))) //nolint:gosec // len(s) <= MaxNameLen, checked above
		h.Write([]byte(s))
	}
	h.Write(serverNonce)
	h.Write(agentNonce)
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum, nil
}

// Hex returns the proof as limits.HexProofSize lowercase hex characters, the
// content of the proof file (FR-A7).
func Hex(proof [sha256.Size]byte) []byte {
	return []byte(hex.EncodeToString(proof[:]))
}

// Equal compares two values in constant time for equal lengths (SEC-03).
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// IsKey reports whether key is a config_push key: user.spire.challenge.
// followed by 16 lowercase hex characters (FR-A8).
func IsKey(key string) bool {
	id, ok := strings.CutPrefix(key, KeyPrefix)
	return ok && len(id) == idLen && isLowerHex([]byte(id))
}

// IsHexProof reports whether value is exactly limits.HexProofSize lowercase
// hex characters (FR-A8, file_pull step 3).
func IsHexProof(value []byte) bool {
	return len(value) == limits.HexProofSize && isLowerHex(value)
}

func isLowerHex(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

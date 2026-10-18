package proof

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// serverNonce and agentNonce are the fixed nonces of the vectors: bytes
// 0x00–0x1f and 0x20–0x3f.
var serverNonce, agentNonce = seq(0), seq(32)

func seq(from byte) []byte {
	b := make([]byte, NonceSize)
	for i := range b {
		b[i] = from + byte(i)
	}
	return b
}

// TestComputeVectors: the digests were computed independently of this code
// (Python's hashlib over the byte layout of attestor spec Proof of
// co-location → Mode file_pull, step 2).
func TestComputeVectors(t *testing.T) {
	for name, tt := range map[string]struct {
		project, instance string
		server, agent     []byte
		want              string
	}{
		"default project":            {"default", "c1", serverNonce, agentNonce, "0a1ef88418b3e6d7b4592aba9f3d6ef26dabc17e4876d73c51abff2434ebc48c"},
		"empty project":              {"", "c1", serverNonce, agentNonce, "13315e8012a3b456a3abd6edebfd16156275e59a40ca0150bdf79dcef1e75a51"},
		"boundary ab|c":              {"ab", "c", serverNonce, agentNonce, "61ce1e34fa9f30bcb90e53e58352d637ec71477a0da74ec9d108b61f9573a6d1"},
		"boundary a|bc":              {"a", "bc", serverNonce, agentNonce, "571438838e1600993ad4071f2f75c364ee745eaac4ca4203f8e36b6d85149330"},
		"names at the 63-byte limit": {strings.Repeat("p", 63), strings.Repeat("n", 63), serverNonce, agentNonce, "c406440de3db79ad30833ec41956c2c42cfd4647947c4747413bf7f429a1dc22"},
		"nonces swapped":             {"default", "c1", agentNonce, serverNonce, "cca4df11e2d172d8fc8260bcb39c856325b032dcdd4d37b2ca23ea520d8425e2"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Compute(tt.project, tt.instance, tt.server, tt.agent)
			if err != nil {
				t.Fatal(err)
			}
			if h := hex.EncodeToString(got[:]); h != tt.want {
				t.Errorf("proof %s, want %s", h, tt.want)
			}
			if h := string(Hex(got)); h != tt.want {
				t.Errorf("Hex %s, want %s", h, tt.want)
			}
		})
	}
}

// TestComputeRefuses: inputs outside the bounds are refused, never
// truncated or padded (SEC-11, FR-S2).
func TestComputeRefuses(t *testing.T) {
	long := strings.Repeat("x", MaxNameLen+1)
	for name, tt := range map[string]struct {
		project, instance string
		server, agent     []byte
	}{
		"empty instance name":  {"default", "", serverNonce, agentNonce},
		"project too long":     {long, "c1", serverNonce, agentNonce},
		"instance too long":    {"default", long, serverNonce, agentNonce},
		"short server nonce":   {"default", "c1", serverNonce[:31], agentNonce},
		"long agent nonce":     {"default", "c1", serverNonce, append(bytes.Clone(agentNonce), 0)},
		"missing server nonce": {"default", "c1", nil, agentNonce},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compute(tt.project, tt.instance, tt.server, tt.agent); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestSEC04Derivation: paths and keys come from hex(SHA-256(agent_nonce))[:16]
// only (SEC-04); the expected id was computed independently.
func TestSEC04Derivation(t *testing.T) {
	id, err := ID(agentNonce)
	if err != nil || id != "72dbb7336c767800" {
		t.Fatalf("ID %q %v", id, err)
	}
	if p, err := Path("/run/spire/lxd", agentNonce); err != nil || p != "/run/spire/lxd/proof-72dbb7336c767800" {
		t.Errorf("Path %q %v", p, err)
	}
	if k, err := Key(agentNonce); err != nil || k != "user.spire.challenge.72dbb7336c767800" {
		t.Errorf("Key %q %v", k, err)
	}
	if _, err := ID(agentNonce[:16]); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ID of a short nonce: %v", err)
	}
	for _, dir := range []string{"", "run/spire/lxd", "/run/spire/../lxd", "/run/spire/lxd/", "/"} {
		if _, err := Path(dir, agentNonce); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Path(%q): %v, want ErrInvalidInput", dir, err)
		}
	}
}

func TestIsKeyAndIsHexProof(t *testing.T) {
	for key, want := range map[string]bool{
		"user.spire.challenge.72dbb7336c767800":  true,
		"user.spire.challenge.72DBB7336C767800":  false,
		"user.spire.challenge.72dbb7336c76780":   false,
		"user.spire.challenge.72dbb7336c7678000": false,
		"user.spire.challenge.72dbb7336c76780g":  false,
		"user.spire.challenge.":                  false,
		"user.lxd-probe.72dbb7336c767800":        false,
	} {
		if got := IsKey(key); got != want {
			t.Errorf("IsKey(%q) = %v", key, got)
		}
	}
	good := strings.Repeat("0a", 32)
	for value, want := range map[string]bool{
		good:                  true,
		strings.ToUpper(good): false,
		good[:63]:             false,
		good + "0":            false,
		good[:63] + "z":       false,
		good[:62] + "\n":      false,
		"":                    false,
	} {
		if got := IsHexProof([]byte(value)); got != want {
			t.Errorf("IsHexProof(%q) = %v", value, got)
		}
	}
}

func TestEqual(t *testing.T) {
	a := []byte(strings.Repeat("a", 64))
	if !Equal(a, bytes.Clone(a)) {
		t.Error("equal values differ")
	}
	b := bytes.Clone(a)
	b[63] = 'b'
	if Equal(a, b) || Equal(a, a[:63]) || Equal(nil, a) {
		t.Error("different values are equal")
	}
}

func TestNewNonce(t *testing.T) {
	a, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != NonceSize || len(b) != NonceSize || bytes.Equal(a, b) {
		t.Errorf("nonces %x %x", a, b)
	}
}

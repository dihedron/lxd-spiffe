package lxdiidv1

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/dihedron/lxd-spiffe/internal/limits"
)

// The decoders face untrusted bytes (SEC-11, T18): they never panic, and
// what they accept re-encodes into a message they accept again.

func FuzzDecodeAttestRequest(f *testing.F) {
	for _, m := range []proto.Message{
		validRequest(),
		&AttestRequest{Version: 1, Claim: &Claim{InstanceName: "c1"}, AgentNonce: make([]byte, 32)},
	} {
		b, _ := proto.Marshal(m)
		f.Add(b)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeAttestRequest(data)
		if err != nil {
			return
		}
		if len(data) > limits.MaxPayload || len(r.GetAgentNonce()) != limits.NonceSize || len(r.GetProofModes()) > limits.MaxProofModes {
			t.Fatalf("accepted out of bounds: %v", r)
		}
		again, err := proto.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeAttestRequest(again); err != nil {
			t.Fatalf("re-encoded request refused: %v", err)
		}
	})
}

func FuzzDecodeChallenge(f *testing.F) {
	for _, m := range []*Challenge{
		{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, ServerNonce: make([]byte, 32), ProofPath: "/run/spire/lxd/proof-72dbb7336c767800", TimeoutMs: 10000},
		{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, ProofKey: "user.spire.challenge.72dbb7336c767800", TimeoutMs: 10000},
	} {
		b, _ := proto.Marshal(m)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := DecodeChallenge(data)
		if err != nil {
			return
		}
		again, _ := proto.Marshal(c)
		if _, err := DecodeChallenge(again); err != nil {
			t.Fatalf("re-encoded challenge refused: %v", err)
		}
	})
}

func FuzzDecodeChallengeResponse(f *testing.F) {
	for _, m := range []*ChallengeResponse{
		{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, Done: true},
		{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: bytes.Repeat([]byte("0a"), 32)},
	} {
		b, _ := proto.Marshal(m)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeChallengeResponse(data)
		if err != nil {
			return
		}
		if r.GetMode() == ProofMode_PROOF_MODE_CONFIG_PUSH && r.GetDone() && len(r.GetEcho()) != limits.HexProofSize {
			t.Fatalf("accepted echo of %d bytes", len(r.GetEcho()))
		}
	})
}

package lxdinstancev1

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func validRequest() *AttestRequest {
	return &AttestRequest{
		Version:       1,
		Claim:         &Claim{Project: "team.a", InstanceName: "c1", CloudInitInstanceId: "01405cc4-dd54-4c91-86ba-9771a1fe64a5", NameSource: ClaimSource_CLAIM_SOURCE_DEVLXD_METADATA},
		AgentNonce:    bytes.Repeat([]byte{7}, 32),
		InstanceType:  InstanceType_INSTANCE_TYPE_CONTAINER,
		ProofModes:    []ProofMode{ProofMode_PROOF_MODE_FILE_PULL, ProofMode_PROOF_MODE_CONFIG_PUSH},
		PluginVersion: "v0.1.0",
	}
}

func marshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// withUnknownField appends field 99 (varint) to an encoded message.
func withUnknownField(b []byte) []byte {
	b = protowire.AppendTag(bytes.Clone(b), 99, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

func TestFRS2DecodeAttestRequest(t *testing.T) {
	got, err := DecodeAttestRequest(marshal(t, validRequest()))
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, validRequest()) {
		t.Errorf("decoded %v", got)
	}
	minimal := &AttestRequest{Version: 1, Claim: &Claim{InstanceName: "c1"}, AgentNonce: make([]byte, 32)}
	if _, err := DecodeAttestRequest(marshal(t, minimal)); err != nil {
		t.Errorf("minimal request: %v", err)
	}

	for name, mutate := range map[string]func(r *AttestRequest){
		"version 0":                 func(r *AttestRequest) { r.Version = 0 },
		"version 2":                 func(r *AttestRequest) { r.Version = 2 },
		"no claim":                  func(r *AttestRequest) { r.Claim = nil },
		"no instance name":          func(r *AttestRequest) { r.Claim.InstanceName = "" },
		"bad instance name":         func(r *AttestRequest) { r.Claim.InstanceName = "c_1" },
		"bad project":               func(r *AttestRequest) { r.Claim.Project = "a/b" },
		"bad cloud-init id":         func(r *AttestRequest) { r.Claim.CloudInitInstanceId = "x" },
		"unknown name source":       func(r *AttestRequest) { r.Claim.NameSource = 9 },
		"short nonce":               func(r *AttestRequest) { r.AgentNonce = r.AgentNonce[:31] },
		"no nonce":                  func(r *AttestRequest) { r.AgentNonce = nil },
		"unknown instance type":     func(r *AttestRequest) { r.InstanceType = 3 },
		"three proof modes":         func(r *AttestRequest) { r.ProofModes = append(r.ProofModes, ProofMode_PROOF_MODE_FILE_PULL) },
		"duplicate proof modes":     func(r *AttestRequest) { r.ProofModes = []ProofMode{1, 1} },
		"unspecified proof mode":    func(r *AttestRequest) { r.ProofModes = []ProofMode{0} },
		"withdrawn proof mode":      func(r *AttestRequest) { r.ProofModes = []ProofMode{3} },
		"plugin version over 128 B": func(r *AttestRequest) { r.PluginVersion = strings.Repeat("v", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			mutate(r)
			if _, err := DecodeAttestRequest(marshal(t, r)); !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v, want ErrInvalid", err)
			}
		})
	}

	t.Run("payload over 4 KiB", func(t *testing.T) {
		r := validRequest()
		big := marshal(t, r)
		big = protowire.AppendTag(big, 100, protowire.BytesType)
		big = protowire.AppendBytes(big, make([]byte, 4096))
		if _, err := DecodeAttestRequest(big); !errors.Is(err, ErrInvalid) {
			t.Errorf("error %v", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		if _, err := DecodeAttestRequest(withUnknownField(marshal(t, validRequest()))); !errors.Is(err, ErrInvalid) {
			t.Errorf("error %v", err)
		}
	})
	t.Run("unknown field in the claim", func(t *testing.T) {
		claim := withUnknownField(marshal(t, &Claim{InstanceName: "c1"}))
		b := protowire.AppendTag(nil, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, claim)
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, make([]byte, 32))
		if _, err := DecodeAttestRequest(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("error %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		if _, err := DecodeAttestRequest([]byte{0xff, 0xff, 0xff}); !errors.Is(err, ErrInvalid) {
			t.Errorf("error %v", err)
		}
	})
}

func TestDecodeChallenge(t *testing.T) {
	filePull := func() *Challenge {
		return &Challenge{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, ServerNonce: make([]byte, 32), ProofPath: "/run/spire/lxd/proof-72dbb7336c767800", TimeoutMs: 10000}
	}
	configPush := func() *Challenge {
		return &Challenge{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, ProofKey: "user.spire.challenge.72dbb7336c767800", TimeoutMs: 10000}
	}
	for _, c := range []*Challenge{filePull(), configPush()} {
		if _, err := DecodeChallenge(marshal(t, c)); err != nil {
			t.Errorf("%v: %v", c.Mode, err)
		}
	}
	for name, c := range map[string]*Challenge{
		"version 2":                 func() *Challenge { c := filePull(); c.Version = 2; return c }(),
		"unspecified mode":          func() *Challenge { c := filePull(); c.Mode = 0; return c }(),
		"withdrawn mode":            func() *Challenge { c := filePull(); c.Mode = 3; return c }(),
		"file_pull short nonce":     func() *Challenge { c := filePull(); c.ServerNonce = c.ServerNonce[:16]; return c }(),
		"file_pull relative path":   func() *Challenge { c := filePull(); c.ProofPath = "run/spire/lxd/proof-72dbb7336c767800"; return c }(),
		"file_pull no path":         func() *Challenge { c := filePull(); c.ProofPath = ""; return c }(),
		"file_pull path over 128 B": func() *Challenge { c := filePull(); c.ProofPath = "/" + strings.Repeat("d", 128); return c }(),
		"file_pull with a key":      func() *Challenge { c := filePull(); c.ProofKey = "user.spire.challenge.72dbb7336c767800"; return c }(),
		"config_push with a nonce":  func() *Challenge { c := configPush(); c.ServerNonce = make([]byte, 32); return c }(),
		"config_push with a path":   func() *Challenge { c := configPush(); c.ProofPath = "/run/spire/lxd/x"; return c }(),
		"config_push foreign key":   func() *Challenge { c := configPush(); c.ProofKey = "user.other"; return c }(),
		"no timeout":                func() *Challenge { c := filePull(); c.TimeoutMs = 0; return c }(),
	} {
		if _, err := DecodeChallenge(marshal(t, c)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error %v, want ErrInvalid", name, err)
		}
	}
	if _, err := DecodeChallenge(withUnknownField(marshal(t, filePull()))); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown field: %v", err)
	}
}

func TestDecodeChallengeResponse(t *testing.T) {
	echo := []byte(strings.Repeat("0a", 32))
	for _, r := range []*ChallengeResponse{
		{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, Done: true},
		{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: echo},
		{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH}, // the guest step failed
		{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL},
	} {
		if _, err := DecodeChallengeResponse(marshal(t, r)); err != nil {
			t.Errorf("%v: %v", r.Mode, err)
		}
	}
	for name, r := range map[string]*ChallengeResponse{
		"version 0":              {Mode: ProofMode_PROOF_MODE_FILE_PULL, Done: true},
		"unspecified mode":       {Version: 1, Done: true},
		"file_pull with an echo": {Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, Done: true, Echo: echo},
		"config_push short echo": {Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: echo[:63]},
		"config_push upper echo": {Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: bytes.ToUpper(echo)},
		"config_push no echo":    {Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true},
		"echo without done":      {Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Echo: echo},
	} {
		if _, err := DecodeChallengeResponse(marshal(t, r)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error %v, want ErrInvalid", name, err)
		}
	}
	if _, err := DecodeChallengeResponse([]byte{0xff}); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage: %v", err)
	}
}

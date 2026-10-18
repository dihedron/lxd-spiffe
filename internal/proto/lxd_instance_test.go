package lxdinstancev1

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// wantSchema is the wire protocol of attestor spec Overview → Wire protocol:
// field numbers MUST NOT change or be reused after the first release, so
// every field, enum value and reservation is pinned here.
const wantSchema = `enum InstanceType: INSTANCE_TYPE_UNSPECIFIED=0 INSTANCE_TYPE_CONTAINER=1 INSTANCE_TYPE_VIRTUAL_MACHINE=2
enum ProofMode: PROOF_MODE_UNSPECIFIED=0 PROOF_MODE_FILE_PULL=1 PROOF_MODE_CONFIG_PUSH=2 reserved 3 reserved PROOF_MODE_READY_SIGNAL
enum ClaimSource: CLAIM_SOURCE_UNSPECIFIED=0 CLAIM_SOURCE_CONFIGURED=1 CLAIM_SOURCE_DEVLXD_METADATA=2 CLAIM_SOURCE_HOSTNAME=3
message Claim: project=1 string, instance_name=2 string, cloud_init_instance_id=3 string, name_source=4 enum
message AttestRequest: version=1 uint32, claim=2 message, agent_nonce=3 bytes, instance_type=4 enum, proof_modes=5 enum repeated, plugin_version=6 string
message Challenge: version=1 uint32, mode=2 enum, server_nonce=3 bytes, proof_path=4 string, proof_key=5 string, timeout_ms=7 uint32 reserved 6 reserved ready_target
message ChallengeResponse: version=1 uint32, mode=2 enum, done=3 bool, echo=4 bytes
`

// TestProtoMatchesSpec: the .proto is the spec's code block, after the
// file's own leading comment, so that neither changes without the other.
func TestProtoMatchesSpec(t *testing.T) {
	spec, err := os.ReadFile("../../.specs/lxd-spire-plugins.md")
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(spec), "\n```proto\n")
	if !ok {
		t.Fatal("the spec has no proto block")
	}
	block, _, ok = strings.Cut(block, "\n```\n")
	if !ok {
		t.Fatal("the spec's proto block is not closed")
	}
	file, err := os.ReadFile("lxd_instance.proto")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(file), "\n\nsyntax")
	if !ok {
		t.Fatal("lxd_instance.proto has no syntax line after its leading comment")
	}
	if got := "syntax" + strings.TrimSuffix(body, "\n"); got != block {
		t.Errorf("lxd_instance.proto differs from the spec's Wire protocol block")
	}
}

func describeEnum(e protoreflect.EnumDescriptor) string {
	var b strings.Builder
	fmt.Fprintf(&b, "enum %s:", e.Name())
	for i := range e.Values().Len() {
		v := e.Values().Get(i)
		fmt.Fprintf(&b, " %s=%d", v.Name(), v.Number())
	}
	for i := range e.ReservedRanges().Len() {
		r := e.ReservedRanges().Get(i)
		fmt.Fprintf(&b, " reserved %d", r[0])
	}
	for i := range e.ReservedNames().Len() {
		fmt.Fprintf(&b, " reserved %s", e.ReservedNames().Get(i))
	}
	return b.String()
}

func describeMessage(m protoreflect.MessageDescriptor) string {
	var fields []string
	for i := range m.Fields().Len() {
		f := m.Fields().Get(i)
		s := fmt.Sprintf("%s=%d %s", f.Name(), f.Number(), f.Kind())
		if f.IsList() {
			s += " repeated"
		}
		fields = append(fields, s)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "message %s: %s", m.Name(), strings.Join(fields, ", "))
	for i := range m.ReservedRanges().Len() {
		fmt.Fprintf(&b, " reserved %d", m.ReservedRanges().Get(i)[0])
	}
	for i := range m.ReservedNames().Len() {
		fmt.Fprintf(&b, " reserved %s", m.ReservedNames().Get(i))
	}
	return b.String()
}

func TestWireSchemaIsStable(t *testing.T) {
	fd := File_internal_proto_lxd_instance_proto
	if got := string(fd.Package()); got != "spire.plugin.lxd_instance.v1" {
		t.Errorf("package %q", got)
	}
	var b strings.Builder
	for i := range fd.Enums().Len() {
		b.WriteString(describeEnum(fd.Enums().Get(i)) + "\n")
	}
	for i := range fd.Messages().Len() {
		b.WriteString(describeMessage(fd.Messages().Get(i)) + "\n")
	}
	if got := b.String(); got != wantSchema {
		t.Errorf("wire schema changed:\n got:\n%s\nwant:\n%s", got, wantSchema)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, m := range []proto.Message{
		&AttestRequest{
			Version:       1,
			Claim:         &Claim{Project: "default", InstanceName: "c1", CloudInitInstanceId: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", NameSource: ClaimSource_CLAIM_SOURCE_DEVLXD_METADATA},
			AgentNonce:    make([]byte, 32),
			InstanceType:  InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE,
			ProofModes:    []ProofMode{ProofMode_PROOF_MODE_FILE_PULL, ProofMode_PROOF_MODE_CONFIG_PUSH},
			PluginVersion: "v0.1.0",
		},
		&Challenge{Version: 1, Mode: ProofMode_PROOF_MODE_FILE_PULL, ServerNonce: make([]byte, 32), ProofPath: "/run/spire/lxd/proof-0123456789abcdef", TimeoutMs: 10000},
		&Challenge{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, ProofKey: "user.spire.challenge.0123456789abcdef", TimeoutMs: 10000},
		&ChallengeResponse{Version: 1, Mode: ProofMode_PROOF_MODE_CONFIG_PUSH, Done: true, Echo: []byte(strings.Repeat("a", 64))},
	} {
		data, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		back := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(data, back); err != nil {
			t.Fatalf("%T: %v", m, err)
		}
		if !proto.Equal(m, back) {
			t.Errorf("%T: round trip changed %v into %v", m, m, back)
		}
	}
}

// TestWithdrawnModeIsNotAValue: the withdrawn ready_signal mode (FR-A9) has
// no Go constant, and its number decodes as an unknown enum value.
func TestWithdrawnModeIsNotAValue(t *testing.T) {
	if _, ok := ProofMode_name[3]; ok {
		t.Error("ProofMode 3 is reserved")
	}
	if slices.Contains([]string{"PROOF_MODE_FILE_PULL", "PROOF_MODE_CONFIG_PUSH", "PROOF_MODE_UNSPECIFIED"}, ProofMode(3).String()) {
		t.Errorf("ProofMode(3) is %s", ProofMode(3))
	}
}

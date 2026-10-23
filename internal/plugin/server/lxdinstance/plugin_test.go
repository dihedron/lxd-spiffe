package lxdinstance

import (
	"context"
	"testing"

	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigure(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("New returned nil")
	}
	for name, hcl := range map[string]string{"empty (FR-S11: no server)": "", "unknown key": `unknown = "x"`} {
		_, err := p.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: hcl, CoreConfiguration: &configv1.CoreConfiguration{TrustDomain: "example.org"}})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("Configure(%s) = %v; want InvalidArgument", name, err)
		}
	}
}

func TestAttest(t *testing.T) {
	if err := New().Attest(nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Attest before Configure = %v; want FailedPrecondition", err)
	}
}

package lxdiid

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
	if _, err := p.Configure(context.Background(), &configv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure(empty): %v", err)
	}
	_, err := p.Configure(context.Background(), &configv1.ConfigureRequest{HclConfiguration: `unknown = "x"`})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Configure(unknown key) = %v; want InvalidArgument", err)
	}
}

func TestAttest(t *testing.T) {
	p := New()
	if err := p.Attest(nil); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Attest before Configure = %v; want FailedPrecondition", err)
	}
	if _, err := p.Configure(context.Background(), &configv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := p.Attest(nil); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Attest = %v; want Unimplemented", err)
	}
}

package probe

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func TestLXDError(t *testing.T) {
	for kind, want := range map[lxd.Kind]int{
		lxd.KindConnection: exit.Connection,
		lxd.KindAuth:       exit.Auth,
		lxd.KindTimeout:    exit.Timeout,
		lxd.KindRedirect:   exit.Internal,
		lxd.KindTooLarge:   exit.Internal,
		lxd.KindProtocol:   exit.Internal,
	} {
		err := LXDError(&lxd.Error{Kind: kind, Err: errors.New("x")})
		if exit.Code(err) != want {
			t.Errorf("%s: exit code = %d; want %d", kind, exit.Code(err), want)
		}
	}
	if LXDError(nil) != nil {
		t.Errorf("LXDError(nil) != nil")
	}
}

// TestLXDErrorKeepsExitCode checks that an error that already carries an
// exit code, such as a refusal inside a session, keeps it.
func TestLXDErrorKeepsExitCode(t *testing.T) {
	err := fmt.Errorf("server config set: %w", exit.New(exit.Refused, errors.New("wrong instance")))
	if code := exit.Code(LXDError(err)); code != exit.Refused {
		t.Errorf("exit %d; want %d", code, exit.Refused)
	}
}

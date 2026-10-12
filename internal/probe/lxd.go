package probe

import (
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// LXDError gives an error of the LXD client the exit code of its kind
// (spec: Exit codes): 3 for connection and TLS failures, 4 for
// authentication and authorization, 7 for timeouts, 1 otherwise.
func LXDError(err error) error {
	if err == nil {
		return nil
	}
	switch lxd.KindOf(err) {
	case lxd.KindConnection:
		return exit.New(exit.Connection, err)
	case lxd.KindAuth:
		return exit.New(exit.Auth, err)
	case lxd.KindTimeout:
		return exit.New(exit.Timeout, err)
	}
	return exit.New(exit.Internal, err)
}

package command

import (
	"github.com/dihedron/lxd-spiffe/internal/command/version"
	"github.com/dihedron/lxd-spiffe/internal/probe/guest"
	"github.com/dihedron/lxd-spiffe/internal/probe/server"
	"github.com/dihedron/lxd-spiffe/internal/probe/util"
)

// Commands is the set of root command groups: one per environment, plus
// version.
type Commands struct {
	// Guest runs inside an LXD instance.
	Guest guest.Commands `command:"guest" description:"Observe LXD from inside an instance (devLXD socket, local files)."`
	// Server talks to the LXD REST API.
	Server server.Commands `command:"server" description:"Observe LXD through its REST API."`
	// Util works offline on evidence files.
	Util util.Commands `command:"util" description:"Offline helpers on evidence files."`
	// Version prints the program version information.
	Version version.Version `command:"version" alias:"v" description:"Print program version information."`
}

// Package guest implements the commands of the guest environment, which run
// inside an LXD instance and use only the devLXD socket and local files
// (spec: Guest environment).
package guest

import (
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// Commands is the command tree of the guest environment.
type Commands struct {
	Preflight Preflight `command:"preflight" description:"Record the guest's view of itself: ids, uid/gid maps, devLXD socket, lxd-agent, /run (PRF-01)."`
	Devlxd    Devlxd    `command:"devlxd" description:"Query the devLXD socket."`
	File      File      `command:"file" description:"Create and inspect files in the lab directory."`
	Idmap     Idmap     `command:"idmap" description:"Inspect the guest's uid/gid maps."`
}

// Devlxd groups the devLXD verbs.
type Devlxd struct {
	Get         DevlxdGet         `command:"get" description:"GET a devLXD path and record the response (PRF-10)."`
	Dump        DevlxdDump        `command:"dump" description:"GET every known devLXD read path (PRF-11)."`
	Watch       DevlxdWatch       `command:"watch" description:"Watch a config key and record when it changes (PRF-12)."`
	Access      DevlxdAccess      `command:"access" description:"Try the devLXD socket as each of the given uids (PRF-13)."`
	AccessChild DevlxdAccessChild `command:"access-child" hidden:"true" description:"Internal: one devLXD call as the current uid, result on stdout (PRF-13)."`
}

// File groups the local file verbs.
type File struct {
	Put    FilePut    `command:"put" description:"Create a file with O_EXCL and record its stat (PRF-20)."`
	Stat   FileStat   `command:"stat" description:"Record lstat and stat of a path (PRF-21)."`
	Rm     FileRm     `command:"rm" description:"Remove a file created by lxd-probe (PRF-22)."`
	Layout FileLayout `command:"layout" description:"Create the hostile layouts used by gate L1 (PRF-23)."`
}

// Idmap groups the idmap verbs.
type Idmap struct {
	Show IdmapShow `command:"show" description:"Record the parsed /proc/self/uid_map and gid_map (PRF-30)."`
}

// DevlxdWatch is guest devlxd watch KEY.
type DevlxdWatch struct {
	probe.GuestCommand
	For  time.Duration `long:"for" description:"How long to watch." default:"60s"`
	Args struct {
		Key string `positional-arg-name:"KEY" description:"Config key to watch."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *DevlxdWatch) Execute(args []string) error {
	return probe.NotImplemented("guest devlxd watch")
}

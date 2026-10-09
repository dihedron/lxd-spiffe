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

// Preflight is guest preflight.
type Preflight struct {
	probe.GuestCommand
}

// Execute runs the command.
func (cmd *Preflight) Execute(args []string) error {
	return probe.NotImplemented("guest preflight")
}

// DevlxdGet is guest devlxd get PATH.
type DevlxdGet struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"devLXD path, e.g. /1.0/config."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *DevlxdGet) Execute(args []string) error {
	return probe.NotImplemented("guest devlxd get")
}

// DevlxdDump is guest devlxd dump.
type DevlxdDump struct {
	probe.GuestCommand
}

// Execute runs the command.
func (cmd *DevlxdDump) Execute(args []string) error {
	return probe.NotImplemented("guest devlxd dump")
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

// DevlxdAccess is guest devlxd access --as-uid N[,N...].
type DevlxdAccess struct {
	probe.GuestCommand
	AsUID probe.UIDList `long:"as-uid" description:"Comma-separated uids to try." required:"yes"`
}

// Execute runs the command.
func (cmd *DevlxdAccess) Execute(args []string) error {
	return probe.NotImplemented("guest devlxd access")
}

// DevlxdAccessChild is the hidden child of guest devlxd access.
type DevlxdAccessChild struct {
	probe.GuestCommand
}

// Execute runs the command.
func (cmd *DevlxdAccessChild) Execute(args []string) error {
	return probe.NotImplemented("guest devlxd access-child")
}

// FilePut is guest file put PATH.
type FilePut struct {
	probe.GuestCommand
	ContentFile string `long:"content-file" description:"File holding the content to write; - reads stdin." required:"yes"`
	Mode        uint32 `long:"mode" description:"File mode, in octal." base:"8" default:"0600"`
	Args        struct {
		Path string `positional-arg-name:"PATH" description:"File to create, under /run/lxd-probe/."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FilePut) Execute(args []string) error {
	return probe.NotImplemented("guest file put")
}

// FileStat is guest file stat PATH.
type FileStat struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"Path to inspect."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileStat) Execute(args []string) error {
	return probe.NotImplemented("guest file stat")
}

// FileRm is guest file rm PATH.
type FileRm struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"File to remove, created by lxd-probe."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FileRm) Execute(args []string) error {
	return probe.NotImplemented("guest file rm")
}

// FileLayout is guest file layout.
type FileLayout struct {
	probe.GuestCommand
}

// Execute runs the command.
func (cmd *FileLayout) Execute(args []string) error {
	return probe.NotImplemented("guest file layout")
}

// IdmapShow is guest idmap show.
type IdmapShow struct {
	probe.GuestCommand
}

// Execute runs the command.
func (cmd *IdmapShow) Execute(args []string) error {
	return probe.NotImplemented("guest idmap show")
}

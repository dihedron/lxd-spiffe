// Package util implements the commands of the util environment, offline
// helpers on evidence files (spec: Util environment).
package util

import (
	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// Commands is the command tree of the util environment.
type Commands struct {
	Diff     Diff     `command:"diff" description:"Compare two evidence records, ignoring volatile fields (PRU-01)."`
	Idmap    Idmap    `command:"idmap" description:"Run and cross-check the attestor's uid translation."`
	Overlap  Overlap  `command:"overlap" description:"Run the device-overlap check on a recorded instance (PRU-04)."`
	Sanitize Sanitize `command:"sanitize" description:"Sanitize an evidence directory, or check that it is sanitized (PRU-05)."`
	Fixtures Fixtures `command:"fixtures" description:"Work with recorded fixtures."`
	Note     Note     `command:"note" description:"Append an operator note or decision to the run (PRU-07)."`
	Report   Report   `command:"report" description:"Render the per-gate Markdown report of one or more runs (PRU-08)."`
}

// Idmap groups the idmap verbs.
type Idmap struct {
	Translate IdmapTranslate `command:"translate" description:"Translate a guest uid to the host uid (PRU-02)."`
	Check     IdmapCheck     `command:"check" description:"Cross-check LXD's idmap keys against the guest's uid_map (PRU-03)."`
}

// Fixtures groups the fixtures verbs.
type Fixtures struct {
	Verify FixturesVerify `command:"verify" description:"Decode every recorded response with the client's typed structs (PRU-06)."`
}

// Overlap is util overlap RECORD.
type Overlap struct {
	probe.GlobalOptions
	ProofDir string `long:"proof-dir" description:"Proof directory to check." default:"/run/spire/lxd"`
	Args     struct {
		Record string `positional-arg-name:"RECORD" description:"Record of server instance show."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *Overlap) Execute(args []string) error {
	return probe.NotImplemented("util overlap")
}

// FixturesVerify is util fixtures verify DIR.
type FixturesVerify struct {
	probe.GlobalOptions
	Args struct {
		Dir string `positional-arg-name:"DIR" description:"Fixtures directory."`
	} `positional-args:"yes" required:"yes"`
}

// Execute runs the command.
func (cmd *FixturesVerify) Execute(args []string) error {
	return probe.NotImplemented("util fixtures verify")
}

// Report is util report RUN_DIR...
type Report struct {
	probe.GlobalOptions
	Args struct {
		RunDirs []string `positional-arg-name:"RUN_DIR" description:"Run directories to merge." required:"1"`
	} `positional-args:"yes"`
}

// Execute runs the command.
func (cmd *Report) Execute(args []string) error {
	return probe.NotImplemented("util report")
}

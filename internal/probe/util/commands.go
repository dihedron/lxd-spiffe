// Package util implements the commands of the util environment, offline
// helpers on evidence files (spec: Util environment).
package util

// Commands is the command tree of the util environment.
type Commands struct {
	Diff     Diff     `command:"diff" description:"Compare two evidence records, ignoring volatile fields (PRU-01)."`
	Idmap    Idmap    `command:"idmap" description:"Run and cross-check the attestor's uid translation."`
	Overlap  Overlap  `command:"overlap" description:"Run the attestor's isolation check on recorded instances (PRU-04)."`
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
	Verify FixturesVerify `command:"verify" description:"Check recorded fixtures: decoding, contract fields, error envelopes (PRU-06)."`
}

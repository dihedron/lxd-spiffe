// Package report renders the per-gate Markdown report of lxd-probe runs
// (spec PRU-08): for each gate its question, the cells covered, the
// observations with their record ids, the decision rules of the runbook as
// text, the operator's recorded decisions, and the open items. It never
// matches observations to rules: deciding is the operator's job, recorded
// with util note --decision.
package report

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

// runbooks is a verbatim copy of the probe spec's "Gate runbooks" section
// (.specs/lxd-probe.md, Testing → Gate runbooks). A test fails when the two
// differ; go test -run TestRunbooksMatchSpec -update refreshes the copy.
//
//go:embed runbooks.md
var runbooks string

// Gate is the text of one gate's runbook.
type Gate struct {
	// ID is the gate, e.g. L1.
	ID string
	// Title is the runbook heading after the gate id.
	Title string
	// Question is the runbook's question, or empty when it has none (the
	// title is the question then).
	Question string
	// Rules is the runbook's decision text, from its Decision label to the
	// end of the runbook, as Markdown.
	Rules string
	// Withdrawn marks a gate kept only for reference stability.
	Withdrawn bool
	// Group marks a known group of records that is not a gate: evidence
	// only, with no decision expected (the stage 3 rehearsals).
	Group bool
	// Axes are the lab-matrix axis values the gate needs at least one real
	// cell for (Operational guidance → Lab matrix → Cells).
	Axes []Axis
}

// Axis is one lab-matrix axis with the values a gate needs. A value is
// covered by a cell whose hyphen-separated name contains it, as c-unpriv in
// lxd6-c-unpriv or lxd6-restricted-c-unpriv.
type Axis struct {
	Name   string
	Values []string
}

var (
	servers   = Axis{"server", []string{"lxd5", "lxd6"}}
	instances = []string{"c-unpriv", "c-priv", "c-iso", "c-raw", "v-ubuntu", "v-debian", "v-alpine"}
)

// axes are the relevant axis values per gate, from the cells each runbook
// names.
var axes = map[string][]Axis{
	"L1":  {servers, {"instance", instances}},
	"L2":  {servers, {"instance", []string{"c-unpriv", "v-ubuntu"}}},
	"L3":  {servers, {"instance", instances}},
	"L4":  {servers, {"instance", []string{"c-unpriv", "c-priv", "v-ubuntu"}}},
	"L6":  {servers, {"instance", []string{"c-unpriv", "v-ubuntu"}}},
	"L7":  {servers, {"instance", []string{"v-ubuntu", "v-debian", "v-alpine", "v-rocky", "v-alma"}}},
	"L8":  {servers},
	"L9":  {servers, {"identity", []string{"read", "files", "edit", "full", "outsider"}}},
	"L11": {{"cluster", []string{"c5", "c6"}}, {"instance", []string{"c-unpriv", "v-ubuntu"}}},
	"L12": {servers},
	"L13": {servers},
	"L14": {servers, {"instance", []string{"c-unpriv", "v-ubuntu"}}},
	"L15": {servers},
	"L16": {servers},
}

// Groups are the known groups of records that are not gates, reported
// after the gates: R, the stage 3 rehearsals (sessions R-1 and R-2, probe
// spec PRS-50 to PRS-53), and S, the stress sessions (stress spec
// STR-43), whose records name gate R or S.
var Groups = []Gate{{
	ID: "R", Title: "Rehearsals of the real proofs (stage 3)", Group: true,
	Question: "Do the attestor's proofs pass end to end in every instance class, and does a tampered run fail at the step it breaks?",
	Axes:     []Axis{servers, {"instance", instances}},
}, {
	ID: "S", Title: "Stress, soak and chaos", Group: true,
	Question: "Does attestation stay correct and recover under sustained load and injected faults?",
	Axes:     []Axis{servers},
}}

var (
	headingRE  = regexp.MustCompile(`(?m)^#### (L\d+) — (.+)$`)
	questionRE = regexp.MustCompile(`\*\*Question:\*\*\s*`)
	decisionRE = regexp.MustCompile(`\*\*Decision[^*]*:\*\*\s*`)
)

// Gates returns the gates of the embedded runbooks, in runbook order.
func Gates() ([]Gate, error) {
	return parseRunbooks(runbooks)
}

// parseRunbooks splits the runbooks section into gates: each "#### Ln —
// Title" heading starts one, which ends at the next heading.
func parseRunbooks(text string) ([]Gate, error) {
	locs := headingRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil, fmt.Errorf("runbooks: no gate headings")
	}
	var gates []Gate
	seen := map[string]bool{}
	for i, loc := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		g := Gate{ID: text[loc[2]:loc[3]], Title: strings.TrimSpace(text[loc[4]:loc[5]])}
		if seen[g.ID] {
			return nil, fmt.Errorf("runbooks: gate %s twice", g.ID)
		}
		seen[g.ID] = true
		body := text[loc[1]:end]
		if m := questionRE.FindStringIndex(body); m != nil {
			q := body[m[1]:]
			if p := strings.Index(q, "\n\n"); p >= 0 {
				q = q[:p]
			}
			g.Question = strings.TrimSpace(q)
		}
		if m := decisionRE.FindStringIndex(body); m != nil {
			g.Rules = strings.TrimSpace(body[m[1]:])
		}
		g.Withdrawn = g.Title == "Withdrawn"
		if g.Rules == "" && !g.Withdrawn {
			return nil, fmt.Errorf("runbooks: gate %s has no decision rules", g.ID)
		}
		g.Axes = axes[g.ID]
		gates = append(gates, g)
	}
	return gates, nil
}

// Call is one call of the attestor's API usage table (attestor spec LXD
// integration → API usage and required extensions), for gate L10.
type Call struct {
	Purpose string
	Method  string
	// Path is the path template, without query.
	Path string
}

// APIUsage is the attestor's API usage table. A test checks it against the
// attestor spec.
var APIUsage = []Call{
	{"Capability check", "GET", "/1.0"},
	{"Resolve instance", "GET", "/1.0/instances/{name}"},
	{"Read proof", "GET", "/1.0/instances/{name}/files"},
	{"Write/remove challenge", "PATCH", "/1.0/instances/{name}"},
	{"Own identity (optional)", "GET", "/1.0/auth/identities/current"},
	{"Uniqueness check", "GET", "/1.0/instances"},
	{"Network project", "GET", "/1.0/projects/{name}"},
}

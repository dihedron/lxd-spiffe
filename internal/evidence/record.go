package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

// Schema is the version of the record and run.json formats.
const Schema = 1

// Outcome is the outcome of a command (PRE-01, PRE-04).
type Outcome string

// Outcomes of a record.
const (
	OutcomeOK         Outcome = "ok"
	OutcomeUnexpected Outcome = "unexpected"
	OutcomeError      Outcome = "error"
)

// Record is the evidence of one command execution (PRE-01).
type Record struct {
	// Schema is the record format version.
	Schema int `json:"schema"`
	// ID is "<env>/<NNNN>", unique within a run.
	ID string `json:"id"`
	// Time is when the command started, in UTC.
	Time time.Time `json:"time"`
	// ToolVersion is the version of lxd-probe.
	ToolVersion string `json:"tool_version"`
	// Env is guest, server or util.
	Env string `json:"env"`
	// Verb is the command below the environment, e.g. "config set".
	Verb string `json:"verb"`
	// Args are the command line arguments after the verb, sanitized.
	Args []string `json:"args"`
	// Gate is the validation gate the record supports.
	Gate string `json:"gate"`
	// Cell is the lab-matrix cell.
	Cell string `json:"cell"`
	// Outcome is ok, unexpected or error.
	Outcome Outcome `json:"outcome"`
	// DurationMS is the duration of the command.
	DurationMS int64 `json:"duration_ms"`
	// Server attributes a server record to a server version (PRE-02).
	Server *ServerInfo `json:"server,omitempty"`
	// Expectation names the expectation the response was checked against (PRE-06).
	Expectation string `json:"expectation,omitempty"`
	// Observations are the structured findings, in order.
	Observations []Observation `json:"observations"`
	// Raw lists the artifacts of the record, relative to the run directory.
	Raw []string `json:"raw"`
}

// ServerInfo identifies the LXD server of a record (PRE-02).
type ServerInfo struct {
	// Version is the LXD server version.
	Version string `json:"version"`
	// APIExtensionsHash is HashExtensions of the server's api_extensions.
	APIExtensionsHash string `json:"api_extensions_hash"`
}

// Observation is one structured finding.
type Observation struct {
	// Key names the finding, e.g. header.X-LXD-uid or expectation.status.
	Key string `json:"key"`
	// Value is any JSON value.
	Value any `json:"value"`
}

// HashExtensions returns a hash of an api_extensions list that does not
// depend on its order.
func HashExtensions(extensions []string) string {
	sorted := slices.Clone(extensions)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

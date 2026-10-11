// Package expect is the expectation table of lxd-probe (spec PRE-04,
// PRE-06): for each API call a verb makes, what the tool expects the
// response to look like. Expectations are data, not code paths; a response
// that deviates from its expectation is a finding, recorded as observations
// with outcome "unexpected".
//
// The entries follow the attestor spec's minimal API contract (LXD
// integration → Minimal API contract), which is itself unconfirmed until
// gate L15: a deviation may mean the contract, not the server, is wrong.
package expect

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// Expectation describes the expected response of one call.
type Expectation struct {
	// Name identifies the expectation in records.
	Name string
	// Method and Path name the call; Path is a template such as
	// /1.0/instances/{name}, Query the parameters that select it.
	Method string
	Path   string
	Query  string
	// Status lists the accepted status codes.
	Status []int
	// Headers must be present in the response.
	Headers []string
	// Envelope is the expected envelope type: sync, async or error.
	Envelope string
	// Fields must be present in the envelope's metadata; nested fields are
	// written with dots, as in environment.server_version.
	Fields []string
}

// Envelope types.
const (
	Sync  = "sync"
	Async = "async"
	Error = "error"
)

// The expectations of the stage-0 server verbs.
var (
	// Server is GET /1.0 (server info, PRS-01).
	Server = Expectation{
		Name: "server", Method: http.MethodGet, Path: "/1.0",
		Status: []int{http.StatusOK}, Envelope: Sync,
		Fields: []string{"api_extensions", "api_version", "auth", "environment.server_version", "environment.server_clustered"},
	}
	// IdentityCurrent is GET /1.0/auth/identities/current (server whoami,
	// PRS-02). Groups and permissions are those of api.IdentityInfo; the
	// attestor spec leaves the field names to be confirmed.
	IdentityCurrent = Expectation{
		Name: "identity-current", Method: http.MethodGet, Path: "/1.0/auth/identities/current",
		Status: []int{http.StatusOK}, Envelope: Sync,
		Fields: []string{"authentication_method", "type", "id", "groups", "effective_permissions"},
	}
	// Instance is GET /1.0/instances/{name}, as the attestor reads it
	// (server instance show, PRS-10).
	Instance = Expectation{
		Name: "instance", Method: http.MethodGet, Path: "/1.0/instances/{name}",
		Status: []int{http.StatusOK}, Headers: []string{"ETag"}, Envelope: Sync,
		Fields: instanceFields,
	}
	// InstanceFull is the same read with recursion=1, which adds the
	// instance state (server instance show, PRS-10).
	InstanceFull = Expectation{
		Name: "instance-full", Method: http.MethodGet, Path: "/1.0/instances/{name}", Query: "recursion=1",
		Status: []int{http.StatusOK}, Headers: []string{"ETag"}, Envelope: Sync,
		Fields: append(slices.Clone(instanceFields), "state"),
	}
)

var instanceFields = []string{
	"name", "project", "status", "status_code", "type", "architecture", "location", "profiles",
	"config", "devices", "expanded_config", "expanded_devices", "created_at", "last_used_at",
}

// RequiredExtensions are the API extensions the attestor requires (attestor
// spec: LXD integration → API usage and required extensions). server info
// reports which of them a server lacks (PRS-01).
var RequiredExtensions = []string{"projects", "instance_generation_id"}

// IdentityExtension is the extension that adds
// GET /1.0/auth/identities/current; server whoami calls it only when the
// server offers it (PRS-02).
const IdentityExtension = "access_management"

// Table lists every expectation, for documentation and tests.
var Table = []Expectation{Server, IdentityCurrent, Instance, InstanceFull}

// Denied returns the expectation of the same call when a 403 is the expected
// result (--expect-denied): status 403, an error envelope, nothing else.
func (e Expectation) Denied() Expectation {
	return Expectation{
		Name: e.Name + "+denied", Method: e.Method, Path: e.Path, Query: e.Query,
		Status: []int{http.StatusForbidden}, Envelope: Error,
	}
}

// Deviation is the value of an expectation.* observation.
type Deviation struct {
	Expectation string `json:"expectation"`
	Want        any    `json:"want"`
	Got         any    `json:"got"`
}

// Check compares a response with the expectation and returns one
// observation per deviation: expectation.status, expectation.header.<name>
// and expectation.body.<field>; none when the response conforms.
func (e Expectation) Check(status int, header http.Header, body []byte) []evidence.Observation {
	var out []evidence.Observation
	add := func(key string, want, got any) {
		out = append(out, evidence.Observation{Key: "expectation." + key, Value: Deviation{Expectation: e.Name, Want: want, Got: got}})
	}
	if !slices.Contains(e.Status, status) {
		add("status", e.Status, status)
	}
	for _, name := range e.Headers {
		if header.Get(name) == "" {
			add("header."+name, "present", "absent")
		}
	}
	var env struct {
		Type     string          `json:"type"`
		Metadata json.RawMessage `json:"metadata"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		add("body", "an LXD envelope", "not JSON")
		return out
	}
	if e.Envelope != "" && env.Type != e.Envelope {
		add("body.type", e.Envelope, env.Type)
	}
	if len(e.Fields) == 0 {
		return out
	}
	var metadata any
	if len(env.Metadata) > 0 {
		dec := json.NewDecoder(bytes.NewReader(env.Metadata))
		dec.UseNumber()
		_ = dec.Decode(&metadata) // a bad metadata has none of the fields
	}
	for _, field := range e.Fields {
		if !has(metadata, field) {
			add("body."+field, "present", "absent")
		}
	}
	return out
}

// has reports whether the dotted field is present in v, null included.
func has(v any, field string) bool {
	for part := range strings.SplitSeq(field, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if v, ok = m[part]; !ok {
			return false
		}
	}
	return true
}

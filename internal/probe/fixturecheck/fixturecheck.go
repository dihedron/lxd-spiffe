// Package fixturecheck checks recorded fixtures (probe spec PRU-06, gate
// L15): each successful response decodes into the type the probe decodes
// it into, carries the fields of the attestor's minimal API contract
// (attestor spec LXD integration → Minimal API contract), and each error
// response is an LXD error envelope. util fixtures verify and the replay
// test of test/fixtures both run it.
package fixturecheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
)

// Kind is what Check did with a fixture.
type Kind string

// Kinds of result.
const (
	Decoded Kind = "decoded" // decoded into a type, and checked
	Checked Kind = "checked" // a body the probe does not decode: headers and envelope checked
	Error   Kind = "error"   // an error response, checked as an envelope
	Skipped Kind = "skipped" // a call the probe neither decodes nor checks
)

// Result is the check of one fixture.
type Result struct {
	Fixture     string   `json:"fixture"`
	Call        string   `json:"call"`
	Kind        Kind     `json:"kind"`
	Type        string   `json:"type,omitempty"`
	DecodeError string   `json:"decode_error,omitempty"`
	Unknown     []string `json:"unknown_fields,omitempty"`
	Missing     []string `json:"missing_fields,omitempty"`
}

// Failed reports whether the result is a finding: a decode error or a
// missing contract field. Unknown fields are information.
func (r Result) Failed() bool { return r.DecodeError != "" || len(r.Missing) > 0 }

// call is one kind of call: how to recognise it, what the probe decodes
// it into, and what the contract requires of a successful answer.
type call struct {
	name   string
	method string
	path   *regexp.Regexp
	query  func(url.Values) bool
	target func() any // nil: not decoded
	fields []string   // metadata fields, dotted for nested ones
	// trusted are fields required only of an answer to a trusted client
	// (auth "trusted"): LXD tells an untrusted one nothing more
	trusted []string
	headers []string
	envType string // the envelope type a success must have, when set
}

var (
	instancePath   = regexp.MustCompile(`^/1\.0/instances/[^/]+$`)
	recursion      = func(q url.Values) bool { return q.Get("recursion") == "1" }
	noRecursion    = func(q url.Values) bool { return q.Get("recursion") != "1" }
	instanceFields = []string{
		"name", "project", "status", "status_code", "type", "architecture", "location", "profiles",
		"config", "devices", "expanded_config", "expanded_devices", "created_at", "last_used_at",
	}
	fileHeaders = []string{"X-LXD-uid", "X-LXD-gid", "X-LXD-mode", "X-LXD-type", "X-LXD-modified"}
)

// serverCalls are the server environment's calls (attestor spec, Minimal
// API contract).
var serverCalls = []call{
	{name: "GET /1.0", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0$`),
		target:  func() any { return &api.Server{} },
		fields:  []string{"api_extensions", "api_version", "auth"},
		trusted: []string{"environment.server_version", "environment.server_clustered"}},
	{name: "GET /1.0/auth/identities/current", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/auth/identities/current$`),
		target: func() any { return &api.IdentityInfo{} },
		fields: []string{"type", "groups", "effective_permissions"}},
	{name: "GET /1.0/instances/{name}?recursion=1", method: http.MethodGet, path: instancePath, query: recursion,
		target: func() any { return &api.InstanceFull{} }, fields: instanceFields, headers: []string{"ETag"}},
	{name: "GET /1.0/instances/{name}", method: http.MethodGet, path: instancePath, query: noRecursion,
		target: func() any { return &api.Instance{} }, fields: instanceFields, headers: []string{"ETag"}},
	{name: "GET /1.0/instances", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/instances$`),
		target: func() any { return &[]api.Instance{} }},
	{name: "GET /1.0/projects/{name}", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/projects/[^/]+$`),
		target: func() any { return &api.Project{} }, fields: []string{"name", "config"}, headers: []string{"ETag"}},
	{name: "GET /1.0/instances/{name}/state", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/instances/[^/]+/state$`),
		target: func() any { return &api.InstanceState{} }},
	// a file's body is not JSON (a directory's is a listing): headers only
	{name: "GET /1.0/instances/{name}/files", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/instances/[^/]+/files$`),
		headers: fileHeaders},
	{name: "HEAD /1.0/instances/{name}/files", method: http.MethodHead, path: regexp.MustCompile(`^/1\.0/instances/[^/]+/files$`),
		headers: fileHeaders},
	{name: "PATCH /1.0/instances/{name}", method: http.MethodPatch, path: instancePath, envType: string(api.SyncResponse)},
	{name: "PUT /1.0/instances/{name}", method: http.MethodPut, path: instancePath, envType: string(api.AsyncResponse),
		target: func() any { return &api.Operation{} }},
	{name: "GET /1.0/operations/{id}/wait", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/operations/[^/]+/wait$`),
		target: func() any { return &api.Operation{} }, fields: []string{"status", "status_code", "err"}},
}

// devlxdCalls are the guest environment's devLXD calls (PRF-10); their
// bodies come plain or in an envelope.
var devlxdCalls = []call{
	{name: "devLXD GET /", method: http.MethodGet, path: regexp.MustCompile(`^/$`), target: func() any { return &[]string{} }},
	{name: "devLXD GET /1.0", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0$`), target: func() any { return &api.DevLXDGet{} }},
	{name: "devLXD GET /1.0/config", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/config$`), target: func() any { return &[]string{} }},
	{name: "devLXD GET /1.0/devices", method: http.MethodGet, path: regexp.MustCompile(`^/1\.0/devices$`),
		target: func() any { return &map[string]map[string]string{} }},
}

func match(calls []call, f evidence.Fixture) (call, bool) {
	q, _ := url.ParseQuery(f.Request.Query)
	for _, c := range calls {
		if c.method == f.Request.Method && c.path.MatchString(f.Request.Path) && (c.query == nil || c.query(q)) {
			return c, true
		}
	}
	return call{name: f.Request.Method + " " + f.Request.Path}, false
}

// Check checks one server fixture.
func Check(f evidence.Fixture) Result {
	c, ok := match(serverCalls, f)
	r := Result{Fixture: f.Name, Call: c.name}
	resp := &lxd.RawResponse{Status: f.Response.Status, Header: f.Response.Header, Body: f.Response.ResponseBody()}
	if lxd.StatusKind(resp.Status) != lxd.KindNone {
		r.Kind = Error
		r.Missing = errorEnvelope(resp.Body)
		return r
	}
	if !ok {
		r.Kind = Skipped
		return r
	}
	for _, h := range c.headers {
		if resp.Header.Get(h) == "" {
			r.Missing = append(r.Missing, "header "+h)
		}
	}
	if c.envType != "" {
		if env, err := resp.Envelope(); err != nil {
			r.DecodeError = err.Error()
		} else if string(env.Type) != c.envType {
			r.Missing = append(r.Missing, fmt.Sprintf("envelope type %s (got %s)", c.envType, env.Type))
		}
	}
	if c.target == nil {
		r.Kind = Checked
		return r
	}
	r.Kind = Decoded
	v := c.target()
	r.Type = fmt.Sprintf("%T", v)
	if err := lxd.Decode(resp, v); err != nil {
		r.DecodeError = err.Error()
		return r
	}
	if env, err := resp.Envelope(); err == nil {
		r.checkFields(env.Metadata, v, c.fields, c.trusted)
	}
	return r
}

// CheckDevLXD checks one devLXD fixture.
func CheckDevLXD(f evidence.Fixture) Result {
	c, ok := match(devlxdCalls, f)
	r := Result{Fixture: f.Name, Call: c.name}
	body := f.Response.ResponseBody()
	if f.Response.Status >= http.StatusBadRequest {
		r.Kind = Error
		return r
	}
	if !ok {
		r.Kind = Skipped
		return r
	}
	// the envelope's metadata, or the plain body (PRF-10)
	var env struct {
		Type     string          `json:"type"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(body, &env) == nil && env.Type != "" {
		body = env.Metadata
	}
	r.Kind = Decoded
	v := c.target()
	r.Type = fmt.Sprintf("%T", v)
	if err := json.Unmarshal(body, v); err != nil {
		r.DecodeError = err.Error()
		return r
	}
	r.checkFields(body, v, c.fields, c.trusted)
	return r
}

// checkFields records the contract fields missing from the raw metadata
// (trusted ones only when auth is "trusted") and the top-level fields the
// type does not know.
func (r *Result) checkFields(raw json.RawMessage, v any, fields, trusted []string) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		if len(fields) > 0 {
			r.Missing = append(r.Missing, "metadata is not an object")
		}
		return
	}
	if m["auth"] == "trusted" {
		fields = append(slices.Clone(fields), trusted...)
	}
	for _, f := range fields {
		if !has(m, strings.Split(f, ".")) {
			r.Missing = append(r.Missing, f)
		}
	}
	known := jsonFields(reflect.TypeOf(v).Elem())
	if known == nil {
		return
	}
	for k := range m {
		if !known[k] {
			r.Unknown = append(r.Unknown, k)
		}
	}
	slices.Sort(r.Unknown)
}

func has(m map[string]any, path []string) bool {
	v, ok := m[path[0]]
	if !ok || len(path) == 1 {
		return ok
	}
	sub, ok := v.(map[string]any)
	return ok && has(sub, path[1:])
}

// jsonFields returns the JSON names of a struct's fields, embedded structs
// included; nil for a type that is not a struct.
func jsonFields(t reflect.Type) map[string]bool {
	if t.Kind() != reflect.Struct {
		return nil
	}
	out := map[string]bool{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
		case f.Anonymous && name == "":
			for k := range jsonFields(f.Type) {
				out[k] = true
			}
		case name != "":
			out[name] = true
		case f.IsExported():
			out[f.Name] = true
		}
	}
	return out
}

// errorEnvelope returns what an error response lacks of an LXD error
// envelope (attestor spec, Minimal API contract).
func errorEnvelope(body []byte) []string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return []string{"error envelope (body is not a JSON object)"}
	}
	var missing []string
	for _, f := range []string{"type", "error", "error_code"} {
		if _, ok := m[f]; !ok {
			missing = append(missing, f)
		}
	}
	return missing
}

// Verify checks every fixture under dir: the server's, and the devLXD
// fixtures of each devlxd/<instance>/ directory below it. Results name
// fixtures by their path relative to dir.
func Verify(dir string) ([]Result, error) {
	var results []Result
	fixtures, err := evidence.LoadFixtures(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range fixtures {
		f.Name = f.Path
		results = append(results, Check(f))
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || d.Name() != "devlxd" {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(path, e.Name())
			devlxd, err := evidence.LoadFixtures(sub)
			if err != nil {
				return err
			}
			prefix, _ := filepath.Rel(dir, sub)
			for _, f := range devlxd {
				f.Name = filepath.ToSlash(filepath.Join(prefix, f.Path))
				results = append(results, CheckDevLXD(f))
			}
		}
		return filepath.SkipDir
	})
	return results, err
}

// Summary counts results by kind, and the findings.
type Summary struct {
	Total   int          `json:"total"`
	Kinds   map[Kind]int `json:"kinds"`
	Failed  []Result     `json:"failed"`
	Unknown []string     `json:"unknown_fields"` // "<type>.<field>", each once
}

// Summarize sums up results.
func Summarize(results []Result) Summary {
	s := Summary{Total: len(results), Kinds: map[Kind]int{}, Failed: []Result{}, Unknown: []string{}}
	for _, r := range results {
		s.Kinds[r.Kind]++
		if r.Failed() {
			s.Failed = append(s.Failed, r)
		}
		for _, u := range r.Unknown {
			if k := r.Type + "." + u; !slices.Contains(s.Unknown, k) {
				s.Unknown = append(s.Unknown, k)
			}
		}
	}
	slices.Sort(s.Unknown)
	return s
}

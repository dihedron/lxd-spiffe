// Package fakelxd is an in-process fake of the LXD REST API for tests
// (spec PRT-10). It has two modes:
//
//   - NewReplay serves recorded fixtures (gate L15) and nothing else;
//   - NewAssumed implements a small LXD by hand. Its behaviour is an
//     assumption, not an observation: it exists only until real fixtures
//     are recorded, every test using it says so in its output, and the
//     plugins' tests never use it.
//
// Both serve HTTPS with a self-signed certificate (see Fingerprint), ask for
// a client certificate and record every request.
package fakelxd

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// Request is a request received by the fake.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	// ClientCert is the SHA-256 fingerprint of the client certificate, if any.
	ClientCert string
}

// Server is a fake LXD.
type Server struct {
	*httptest.Server
	t testing.TB

	mu        sync.Mutex
	requests  []Request
	instances map[string]*instance // key: project/name
	files     map[string]*File     // key: project/name/path
	deny      []string             // path prefixes answered with 403
	nextOp    int
	replay    map[string][]evidence.Fixture
}

// File is a file inside a fake instance, as the files API reports it.
type File struct {
	Content string
	UID     int
	GID     int
	Mode    int
	Type    string
}

type instance struct {
	Name    string
	Project string
	Status  string
	Config  map[string]string
	etagSeq int
}

func (i *instance) etag() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%s/%d", i.Project, i.Name, i.etagSeq))
	return hex.EncodeToString(sum[:8])
}

// NewAssumed starts a hand-written fake LXD and logs that the test relies
// on assumed, not recorded, behaviour.
func NewAssumed(t testing.TB) *Server {
	t.Helper()
	t.Log("fakelxd: ASSUMPTION-BASED fake LXD (PRT-10); replace with recorded fixtures after Lab 0")
	s := &Server{t: t, instances: map[string]*instance{}, files: map[string]*File{}}
	s.start(http.HandlerFunc(s.assumed))
	return s
}

// NewReplay starts a fake LXD that answers only with the fixtures under
// dir. Requests with the same method, path and query are answered in the
// order of the fixture names; a request without a fixture fails the test.
func NewReplay(t testing.TB, dir string) *Server {
	t.Helper()
	fixtures, err := evidence.LoadFixtures(dir)
	if err != nil {
		t.Fatalf("fakelxd: %v", err)
	}
	s := &Server{t: t, replay: map[string][]evidence.Fixture{}}
	for _, f := range fixtures {
		key := replayKey(f.Request.Method, f.Request.Path, f.Request.Query)
		s.replay[key] = append(s.replay[key], f)
	}
	s.start(http.HandlerFunc(s.replayed))
	return s
}

func (s *Server) start(h http.Handler) {
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		h.ServeHTTP(w, r)
	}))
	s.Server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	s.Server.StartTLS()
	s.t.Cleanup(s.Close)
}

// Fingerprint returns the SHA-256 fingerprint of the server certificate,
// as given to --pin.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// AddInstance adds a running instance.
func (s *Server) AddInstance(project, name string, config map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[project+"/"+name] = &instance{Name: name, Project: project, Status: "Running", Config: maps.Clone(config)}
}

// AddFile adds a file to an instance.
func (s *Server) AddFile(project, name, path string, f File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[project+"/"+name+"/"+path] = &f
}

// Deny answers 403 to every request whose path starts with prefix.
func (s *Server) Deny(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = append(s.deny, prefix)
}

// BumpETag changes an instance's ETag without changing its config, as an
// LXD-internal volatile update would.
func (s *Server) BumpETag(project, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[project+"/"+name]; ok {
		i.etagSeq++
	}
}

func (s *Server) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	req := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		req.ClientCert = hex.EncodeToString(sum[:])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
}

func replayKey(method, path, query string) string {
	values, _ := url.ParseQuery(query)
	return method + " " + path + "?" + values.Encode()
}

func (s *Server) replayed(w http.ResponseWriter, r *http.Request) {
	key := replayKey(r.Method, r.URL.Path, r.URL.RawQuery)
	s.mu.Lock()
	queue := s.replay[key]
	if len(queue) == 0 {
		s.mu.Unlock()
		s.t.Errorf("fakelxd: no fixture for %s", key)
		writeError(w, http.StatusNotImplemented, "no fixture for "+key)
		return
	}
	f := queue[0]
	if len(queue) > 1 {
		s.replay[key] = queue[1:]
	}
	s.mu.Unlock()
	for name, values := range f.Response.Header {
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		w.Header()[name] = values
	}
	w.WriteHeader(f.Response.Status)
	w.Write(f.Response.ResponseBody()) //nolint:errcheck // test server
}

func (s *Server) assumed(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, prefix := range s.deny {
		if strings.HasPrefix(r.URL.Path, prefix) {
			writeError(w, http.StatusForbidden, "not authorized")
			return
		}
	}
	project := r.URL.Query().Get("project")
	if project == "" {
		project = "default"
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/1.0" && r.Method == http.MethodGet:
		writeSync(w, map[string]any{
			"api_extensions": []string{"projects", "instance_generation_id"},
			"api_version":    "1.0",
			"auth":           "trusted",
			"environment":    map[string]any{"server_version": "6.5", "server_clustered": false, "server_name": "fake"},
		}, "")
	case len(parts) == 3 && parts[1] == "instances":
		s.instance(w, r, project, parts[2])
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "files" && r.Method == http.MethodGet:
		s.file(w, r, project, parts[2])
	case len(parts) == 4 && parts[1] == "operations" && parts[3] == "wait":
		writeSync(w, map[string]any{"id": parts[2], "status": "Success", "status_code": 200, "err": ""}, "")
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) instance(w http.ResponseWriter, r *http.Request, project, name string) {
	i, ok := s.instances[project+"/"+name]
	if !ok {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeSync(w, map[string]any{
			"name": i.Name, "project": i.Project, "status": i.Status, "status_code": 103,
			"type": "container", "config": i.Config, "location": "none",
		}, i.etag())
	case http.MethodPatch, http.MethodPut:
		if match := r.Header.Get("If-Match"); match != "" && match != i.etag() {
			writeError(w, http.StatusPreconditionFailed, "ETag doesn't match")
			return
		}
		var body struct {
			Config map[string]*string `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad body")
			return
		}
		if r.Method == http.MethodPut {
			i.Config = map[string]string{}
		}
		for k, v := range body.Config {
			if v == nil || *v == "" {
				delete(i.Config, k)
			} else {
				i.Config[k] = *v
			}
		}
		i.etagSeq++
		s.nextOp++
		op := fmt.Sprintf("/1.0/operations/op-%d", s.nextOp)
		w.Header().Set("Location", op)
		writeJSON(w, http.StatusAccepted, map[string]any{"type": "async", "status": "Operation created", "status_code": 100, "operation": op, "metadata": map[string]any{"id": fmt.Sprintf("op-%d", s.nextOp)}})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) file(w http.ResponseWriter, r *http.Request, project, name string) {
	if _, ok := s.instances[project+"/"+name]; !ok {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}
	f, ok := s.files[project+"/"+name+"/"+r.URL.Query().Get("path")]
	if !ok {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	w.Header().Set("X-LXD-uid", fmt.Sprint(f.UID))
	w.Header().Set("X-LXD-gid", fmt.Sprint(f.GID))
	w.Header().Set("X-LXD-mode", fmt.Sprintf("%04o", f.Mode))
	w.Header().Set("X-LXD-type", f.Type)
	w.Header().Set("Content-Type", "application/octet-stream")
	io.WriteString(w, f.Content) //nolint:errcheck // test server
}

func writeSync(w http.ResponseWriter, metadata any, etag string) {
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "sync", "status": "Success", "status_code": 200, "metadata": metadata})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": msg, "error_code": status, "metadata": nil})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // test server
}

package fakelxd

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
)

// DevLXD is an assumption-based devLXD on a Unix socket, modelled on what
// LXD 5.21 and 6.9 answered in the lab: devLXD's plain format (the content
// itself, JSON or text) with text errors, only user.* and cloud-init.* keys
// readable (403 for others), and /1.0/events never answering, like a
// stream. Real fixtures (the lab's S0-2) supersede it. Set its fields
// before the first request.
type DevLXD struct {
	// Socket is the socket's path.
	Socket string
	// InstanceType and Location are what GET /1.0 reports.
	InstanceType, Location string
	// Config holds the instance's config keys, readable or not.
	Config map[string]string
	// MetaData is GET /1.0/meta-data.
	MetaData string
	// Devices is GET /1.0/devices.
	Devices map[string]map[string]string
	// Envelope wraps answers in the LXD envelope, as over vsock.
	Envelope bool

	mu       sync.Mutex
	requests []string
}

// NewDevLXD starts a fake devLXD for a container named c1.
func NewDevLXD(t testing.TB) *DevLXD {
	t.Helper()
	dir, err := os.MkdirTemp("", "devlxd") // short: socket paths are limited
	if err != nil {
		t.Fatalf("fakelxd: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	d := &DevLXD{
		Socket:       filepath.Join(dir, "sock"),
		InstanceType: "container",
		Location:     "lxd6",
		Config:       map[string]string{},
		MetaData:     "#cloud-config\ninstance-id: 7c64d5b4-63d7-4bd6-9a5e-0d3b5a1c2f10\nlocal-hostname: c1\n",
		Devices: map[string]map[string]string{
			"eth0": {"type": "nic", "network": "lxdbr0", "name": "eth0", "hwaddr": "00:16:3e:0e:4b:fd"},
			"root": {"type": "disk", "path": "/", "pool": "default"},
		},
	}
	l, err := net.Listen("unix", d.Socket)
	if err != nil {
		t.Fatalf("fakelxd: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve), ReadHeaderTimeout: time.Second}
	go srv.Serve(l)                   //nolint:errcheck
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck
	return d
}

// Requests returns the paths requested so far.
func (d *DevLXD) Requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.requests)
}

// readable reports whether devLXD exposes the key to the guest.
func readable(key string) bool {
	return strings.HasPrefix(key, "user.") || strings.HasPrefix(key, "cloud-init.")
}

func (d *DevLXD) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests = append(d.requests, r.URL.Path)
	d.mu.Unlock()
	if r.Method != http.MethodGet {
		d.text(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := r.URL.Path
	switch {
	case path == "/":
		d.json(w, []string{"/1.0"})
	case path == "/1.0":
		d.json(w, map[string]any{
			"api_version": "1.0", "instance_type": d.InstanceType, "location": d.Location,
			"state": "Started", "auth": "untrusted", "supported_storage_drivers": []string{},
		})
	case path == "/1.0/config":
		urls := []string{}
		for key := range d.Config {
			if readable(key) {
				urls = append(urls, "/1.0/config/"+key)
			}
		}
		slices.Sort(urls)
		d.json(w, urls)
	case strings.HasPrefix(path, "/1.0/config/"):
		key := strings.TrimPrefix(path, "/1.0/config/")
		value, ok := d.Config[key]
		if !readable(key) {
			d.text(w, http.StatusForbidden, "Forbidden")
		} else if !ok {
			d.text(w, http.StatusNotFound, "not found")
		} else {
			d.content(w, value)
		}
	case path == "/1.0/meta-data":
		d.content(w, d.MetaData)
	case path == "/1.0/devices":
		d.json(w, d.Devices)
	case path == "/1.0/events":
		<-r.Context().Done() // a stream: nothing until the client gives up
	default:
		d.text(w, http.StatusNotFound, "not found")
	}
}

// json answers v as JSON, or as the metadata of an envelope.
func (d *DevLXD) json(w http.ResponseWriter, v any) {
	if d.Envelope {
		writeSync(w, v, "")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// content answers a text value, or the value as the metadata of an
// envelope.
func (d *DevLXD) content(w http.ResponseWriter, text string) {
	if d.Envelope {
		writeSync(w, text, "")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(text)) //nolint:errcheck // test server
}

// text answers an error as text, or as an error envelope.
func (d *DevLXD) text(w http.ResponseWriter, status int, msg string) {
	if d.Envelope {
		writeError(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	w.Write([]byte(msg)) //nolint:errcheck // test server
}

// NewDevLXDReplay starts a devLXD on a Unix socket that answers only with
// the fixtures under dir, as NewReplay does for the LXD API, and returns
// the socket's path.
func NewDevLXDReplay(t testing.TB, dir string) string {
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
	tmp, err := os.MkdirTemp("", "devlxd")
	if err != nil {
		t.Fatalf("fakelxd: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) }) //nolint:errcheck
	socket := filepath.Join(tmp, "sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("fakelxd: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.replayed), ReadHeaderTimeout: time.Second}
	go srv.Serve(l)                   //nolint:errcheck
	t.Cleanup(func() { srv.Close() }) //nolint:errcheck
	return socket
}

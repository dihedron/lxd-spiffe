// Package fixtures_test replays the committed fixtures: the sanitized
// responses of real LXD servers that the lab's S0-1 session records (probe
// spec PRT-10, gate L15). Each one is requested from a fake LXD that
// replays them, through the same client lxd-probe uses, and decoded into the
// LXD type the probe decodes it into; a decode failure calls for a client
// struct fix. The devLXD fixtures under <version>/devlxd/<instance>/ (S0-2)
// replay through the probe's devLXD client the same way. It stands in for
// "util fixtures verify" until stage 2.
package fixtures_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/canonical/lxd/shared/api"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

var instancePath = regexp.MustCompile(`^/1\.0/instances/[^/]+$`)

// decodeInto returns what the probe decodes a successful response of the
// request into, or nil for a request it does not decode.
func decodeInto(path string, query url.Values) any {
	switch {
	case path == "/1.0":
		return &api.Server{}
	case path == "/1.0/auth/identities/current":
		return &api.IdentityInfo{}
	case instancePath.MatchString(path) && query.Get("recursion") == "1":
		return &api.InstanceFull{}
	case instancePath.MatchString(path):
		return &api.Instance{}
	}
	return nil
}

func TestReplay(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		t.Skip("no fixtures yet: they come from the lab's S0-1 session (lab.sh test --collect)")
	}
	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			fixtures, err := evidence.LoadFixtures(version)
			if err != nil {
				t.Fatal(err)
			}
			if len(fixtures) == 0 {
				t.Fatal("no fixtures")
			}
			fake := fakelxd.NewReplay(t, version)
			clients := map[string]*lxd.Client{}
			ctx := context.Background()
			for _, f := range fixtures {
				query, err := url.ParseQuery(f.Request.Query)
				if err != nil {
					t.Fatalf("%s: query: %v", f.Name, err)
				}
				project := query.Get("project")
				query.Del("project")
				c, ok := clients[project]
				if !ok {
					if c, err = lxd.Connect(ctx, lxd.Config{Endpoint: fake.URL, Pin: fake.Fingerprint(), Project: project}); err != nil {
						t.Fatalf("%s: %v", f.Name, err)
					}
					clients[project] = c
				}
				resp, err := c.Raw(ctx, lxd.RawRequest{Method: f.Request.Method, Path: f.Request.Path, Query: query})
				if err != nil {
					t.Fatalf("%s: %v", f.Name, err)
				}
				if resp.Status != f.Response.Status {
					t.Errorf("%s: status %d, fixture %d", f.Name, resp.Status, f.Response.Status)
					continue
				}
				if resp.Status >= http.StatusBadRequest {
					if env, err := resp.Envelope(); err != nil || env.Type != api.ErrorResponse {
						t.Errorf("%s: error response without an error envelope: %v", f.Name, err)
					}
					continue
				}
				if v := decodeInto(f.Request.Path, query); v != nil {
					if err := lxd.Decode(resp, v); err != nil {
						t.Errorf("%s: decoding into %T: %v", f.Name, v, err)
					}
				}
			}
			replayDevLXD(t, filepath.Join(version, "devlxd"))
		})
	}
}

// devlxdInto returns what the probe decodes a successful devLXD response
// into (guest devlxd get, PRE-08), or nil for a text body.
func devlxdInto(path string) any {
	switch path {
	case "/1.0":
		return &api.DevLXDGet{}
	case "/1.0/devices":
		return &map[string]map[string]string{}
	case "/", "/1.0/config":
		return &[]string{}
	}
	return nil
}

// replayDevLXD replays the devLXD fixtures of each instance under dir (the
// lab's S0-2) through the probe's devLXD client, and decodes them.
func replayDevLXD(t *testing.T, dir string) {
	instances, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		t.Run("devlxd/"+inst.Name(), func(t *testing.T) {
			path := filepath.Join(dir, inst.Name())
			fixtures, err := evidence.LoadFixtures(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(fixtures) == 0 {
				t.Fatal("no fixtures")
			}
			c, err := devlxd.Connect(context.Background(), devlxd.Config{Socket: fakelxd.NewDevLXDReplay(t, path)})
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range fixtures {
				resp, err := c.Get(context.Background(), f.Request.Path)
				if err != nil {
					t.Fatalf("%s: %v", f.Name, err)
				}
				if resp.Status != f.Response.Status {
					t.Errorf("%s: status %d, fixture %d", f.Name, resp.Status, f.Response.Status)
					continue
				}
				if v := devlxdInto(f.Request.Path); v != nil && resp.Status < http.StatusBadRequest {
					if err := resp.Decode(v); err != nil {
						t.Errorf("%s: decoding into %T: %v", f.Name, v, err)
					}
				}
			}
		})
	}
}

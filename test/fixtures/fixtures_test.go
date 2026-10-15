// Package fixtures_test replays the committed fixtures: the sanitized
// responses of real LXD servers that the lab's sessions record (probe
// spec PRT-10, gate L15). Each one is requested from a fake LXD that
// replays them, through the same client lxd-probe uses (PRE-12), and the
// fixtures are checked by the code of "util fixtures verify"
// (internal/probe/fixturecheck, PRU-06): a decode failure calls for a
// client struct fix, a missing field for a contract fix. The devLXD
// fixtures under <version>/devlxd/<instance>/ (S0-2) replay through the
// probe's devLXD client the same way.
package fixtures_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe/fixturecheck"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// verify runs util fixtures verify's checks on the version's fixtures.
func verify(t *testing.T, version string) {
	t.Helper()
	results, err := fixturecheck.Verify(version)
	if err != nil {
		t.Fatal(err)
	}
	sum := fixturecheck.Summarize(results)
	for _, r := range sum.Failed {
		t.Errorf("%s (%s): decode %q, missing %v", r.Fixture, r.Call, r.DecodeError, r.Missing)
	}
	t.Logf("%d fixtures: %v; fields the SDK's types do not know: %v", sum.Total, sum.Kinds, sum.Unknown)
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
				if !bytes.Equal(resp.Body, f.Response.ResponseBody()) {
					t.Errorf("%s: the replayed body differs from the fixture", f.Name)
				}
			}
			replayDevLXD(t, filepath.Join(version, "devlxd"))
			verify(t, version)
		})
	}
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
			}
		})
	}
}

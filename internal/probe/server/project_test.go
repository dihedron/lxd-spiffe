package server

import (
	"bytes"
	"slices"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

// TestProjectShow is PRS-13: the project, its config, and features.networks
// as its own observation, for gate L16.
func TestProjectShow(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddProject("l16-on", map[string]string{"features.networks": "true", "user.secret": "hunter2"})
	fake.AddProject("l16-unset", map[string]string{"features.images": "true"})
	for name, want := range map[string]any{"l16-on": "true", "l16-unset": "unset"} {
		t.Run(name, func(t *testing.T) {
			opts := options(t, fake)
			cmd := &ProjectShow{ServerCommand: opts, stdout: &bytes.Buffer{}}
			cmd.Args.Name = name
			if err := cmd.Execute(nil); err != nil {
				t.Fatal(err)
			}
			rec := record(t, opts, 1)
			if rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Expectations, []string{"project"}) {
				t.Fatalf("outcome %q, expectations %v, observations %v", rec.Outcome, rec.Expectations, rec.Observations)
			}
			if got := observation(t, rec, "project.features.networks"); got != want {
				t.Errorf("features.networks %v, want %v", got, want)
			}
			if observation(t, rec, "project.etag") == "" || observation(t, rec, "project.name") != name {
				t.Errorf("observations %v", rec.Observations)
			}
			if name == "l16-on" {
				config, _ := observation(t, rec, "project.config").(map[string]any)
				if config["user.secret"] != evidence.Redacted {
					t.Errorf("config %v", config)
				}
				golden(t, "project-show", rec)
			}
			reqs := fake.Requests()
			if last := reqs[len(reqs)-1]; last.Path != "/1.0/projects/"+name {
				t.Errorf("request %+v", last)
			}
		})
	}
}

func TestProjectShowRefusals(t *testing.T) {
	fake := fakelxd.NewAssumed(t)
	fake.AddProject("restricted", nil)
	fake.HideProject("restricted")
	fake.AddProject("denied", nil)
	fake.Deny("/1.0/projects/denied")
	for name, tt := range map[string]struct {
		project        string
		hidden, denied bool
		want           evidence.Outcome
	}{
		"missing project is a finding": {"missing", false, false, evidence.OutcomeUnexpected},
		"hidden, expected":             {"restricted", true, false, evidence.OutcomeOK},
		"denied, expected":             {"denied", false, true, evidence.OutcomeOK},
	} {
		t.Run(name, func(t *testing.T) {
			opts := options(t, fake)
			opts.ExpectHidden, opts.ExpectDenied = tt.hidden, tt.denied
			cmd := &ProjectShow{ServerCommand: opts, stdout: &bytes.Buffer{}}
			cmd.Args.Name = tt.project
			if err := cmd.Execute(nil); err != nil {
				t.Fatal(err)
			}
			if rec := record(t, opts, 1); rec.Outcome != tt.want {
				t.Errorf("outcome %q, observations %v", rec.Outcome, rec.Observations)
			}
		})
	}
}

package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

func runChild(t *testing.T, sock string, paths ...string) childReport {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := &DevlxdAccessChild{GuestCommand: probe.GuestCommand{Socket: sock}, Path: paths, stdout: out}
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("child: %v", err)
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("the child printed a body: %s", out.String())
	}
	var rep childReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("child output %q: %v", out.String(), err)
	}
	return rep
}

func TestAccessChild(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.Config["cloud-init.user-data"] = secret
	rep := runChild(t, fake.Socket, "/1.0", "/1.0/config/cloud-init.user-data", "/1.0/config/volatile.uuid")
	want := []childResult{
		{Path: "/1.0", Status: 200, Format: "plain"},
		{Path: "/1.0/config/cloud-init.user-data", Status: 200, Format: "plain"},
		{Path: "/1.0/config/volatile.uuid", Status: 403, Format: "plain", Error: "Forbidden"},
	}
	if !slices.Equal(rep.Results, want) {
		t.Errorf("results %+v; want %+v", rep.Results, want)
	}

	// no socket: every path reports the error, and the child still answers
	rep = runChild(t, filepath.Join(t.TempDir(), "absent"), "/1.0")
	if len(rep.Results) != 1 || rep.Results[0].Status != 0 || rep.Results[0].Error == "" {
		t.Errorf("no socket: %+v", rep.Results)
	}
}

// stubAccess replaces the root check and the child runner for the test.
func stubAccess(t *testing.T, root bool, run func(ctx context.Context, uid, gid uint32, args []string) ([]byte, error)) {
	t.Helper()
	oldRoot, oldRun := isRoot, startChild
	isRoot = func() bool { return root }
	startChild = run
	t.Cleanup(func() { isRoot, startChild = oldRoot, oldRun })
}

func TestAccess(t *testing.T) {
	var calls []string
	stubAccess(t, true, func(ctx context.Context, uid, gid uint32, args []string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch uid {
		case 0:
			return json.Marshal(childReport{UID: 0, GID: 0, Results: []childResult{{Path: "/1.0", Status: 200, Format: "plain"}}})
		case 1000:
			return json.Marshal(childReport{UID: 1000, GID: 1000, Results: []childResult{{Path: "/1.0", Status: 401, Format: "plain", Error: "Access denied for non-root user"}}})
		}
		return nil, errors.New("fork/exec: operation not permitted")
	})
	out := &bytes.Buffer{}
	cmd := &DevlxdAccess{GuestCommand: probe.GuestCommand{GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir(), RunID: "r1", JSON: true}, Socket: "/dev/lxd/sock"},
		AsUID: probe.UIDList{0, 1000, 65534}, Path: []string{"/1.0"}, stdout: out}
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := records(t, out)[0]
	if rec.Verb != "devlxd access" || rec.Outcome != evidence.OutcomeOK || !slices.Equal(rec.Args, []string{"--as-uid", "0,1000,65534", "--path", "/1.0"}) {
		t.Errorf("record %q %s %v", rec.Verb, rec.Outcome, rec.Args)
	}
	if !strings.Contains(calls[0], "guest devlxd access-child --socket /dev/lxd/sock --path /1.0") {
		t.Errorf("child arguments %q", calls[0])
	}
	for key, want := range map[string]string{
		"access.uid.0":     `{"gid":0,"results":[{"allowed":true,"format":"plain","path":"/1.0","status":200}]}`,
		"access.uid.1000":  `{"gid":1000,"results":[{"allowed":false,"error":"Access denied for non-root user","format":"plain","path":"/1.0","status":401}]}`,
		"access.uid.65534": `{"error":"fork/exec: operation not permitted","gid":65534}`,
	} {
		if got := jsonOf(value(t, rec, key)); got != want {
			t.Errorf("%s = %s; want %s", key, got, want)
		}
	}
}

func TestAccessNeedsRoot(t *testing.T) {
	stubAccess(t, false, nil)
	cmd := &DevlxdAccess{GuestCommand: probe.GuestCommand{GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir()}}, AsUID: probe.UIDList{1000}, Path: []string{"/1.0"}, stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("as non-root: exit %d (%v)", exit.Code(err), err)
	}
}

func TestPrimaryGID(t *testing.T) {
	if gid := primaryGID(0); gid != 0 {
		t.Errorf("primaryGID(0) = %d", gid)
	}
	if gid := primaryGID(4242424); gid != 4242424 {
		t.Errorf("a uid without a passwd entry: gid %d; want the same number", gid)
	}
}

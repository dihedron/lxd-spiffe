package util

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func TestNote(t *testing.T) {
	opts := utilOpts(t)
	opts.Gate, opts.Cell = "L9", "lxd6-c-unpriv"
	out := &bytes.Buffer{}
	cmd := &Note{GlobalOptions: opts, stdout: out}
	cmd.Args.Text = "lxc file pull as read returned 403; NIC 00:16:3e:0e:4b:fd"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	rec := lastRecord(t, out)
	if rec.Verb != "note" || rec.Env != "util" || rec.Gate != "L9" || rec.Cell != "lxd6-c-unpriv" {
		t.Errorf("record %q %q %q %q", rec.Verb, rec.Env, rec.Gate, rec.Cell)
	}
	if obs(t, rec, "note.kind") != "observation" {
		t.Errorf("kind %v", obs(t, rec, "note.kind"))
	}
	text, _ := obs(t, rec, "note.text").(string)
	if !strings.Contains(text, "returned 403") || strings.Contains(text, "00:16:3e") {
		t.Errorf("text %q: kept, with its MAC address aliased", text)
	}

	// a decision
	out = &bytes.Buffer{}
	cmd = &Note{GlobalOptions: opts, Decision: true, stdout: out}
	cmd.Args.Text = "only root can use devLXD: recorded as a control"
	if err := cmd.Execute(nil); err != nil {
		t.Fatal(err)
	}
	if rec := lastRecord(t, out); obs(t, rec, "note.kind") != "decision" || !strings.Contains(strings.Join(rec.Args, " "), "--decision") {
		t.Errorf("decision: %v %v", rec.Args, rec.Observations)
	}

	// a decision belongs to a gate
	noGate := utilOpts(t)
	cmd = &Note{GlobalOptions: noGate, Decision: true, stdout: &bytes.Buffer{}}
	cmd.Args.Text = "x"
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("--decision without --gate: exit %d (%v)", exit.Code(err), err)
	}
}

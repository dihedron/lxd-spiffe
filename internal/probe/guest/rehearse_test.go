package guest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
	"github.com/dihedron/lxd-spiffe/internal/proof"
	"github.com/dihedron/lxd-spiffe/test/fakelxd"
)

func fileChallenge(t *testing.T) (rehearse.Challenge, []byte, []byte) {
	t.Helper()
	sn, an := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	p, err := proof.Path("/run/spire/lxd", an)
	if err != nil {
		t.Fatal(err)
	}
	return rehearse.Challenge{
		Mode: rehearse.FilePull, Project: "default", Instance: "c-unpriv",
		ServerNonce: strings.Repeat("01", 32), AgentNonce: strings.Repeat("02", 32), ProofPath: p, TimeoutMS: 30000,
	}, sn, an
}

func challengeLine(t *testing.T, c rehearse.Challenge) string {
	t.Helper()
	var b bytes.Buffer
	if err := rehearse.WriteChallenge(&b, c); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestPRF24RehearseFilePull: the guest writes the proof internal/proof
// computes from the challenge, at its path, mode 0600, with put's marker,
// and answers done.
func TestPRF24RehearseFilePull(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		root, opts := fileTest(t)
		opts.AllowSpirePaths = true
		c, sn, an := fileChallenge(t)
		out := &bytes.Buffer{}
		cmd := &RehearseFilePull{GuestCommand: opts, Challenge: challengeLine(t, c), Tamper: tamper, stdout: out}
		if err := cmd.Execute(nil); err != nil {
			t.Fatalf("tamper %v: %v", tamper, err)
		}
		file := filepath.Join(root, strings.TrimPrefix(c.ProofPath, "/"))
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := proof.Compute("default", "c-unpriv", sn, an)
		if got := proof.Equal(data, proof.Hex(want)); got == tamper {
			t.Errorf("tamper %v: proof matches %v", tamper, got)
		}
		if !proof.IsHexProof(data) {
			t.Errorf("content %q", data)
		}
		if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %o", fi.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(file), markerName(filepath.Base(file)))); err != nil {
			t.Errorf("marker: %v", err)
		}
		a, err := rehearse.ReadAnswer(context.Background(), bytes.NewReader(out.Bytes()))
		if err != nil || a != (rehearse.Answer{Mode: rehearse.FilePull, Done: true}) {
			t.Errorf("answer %+v, %v", a, err)
		}
		notIn(t, string(data), opts.EvidenceDir)
	}
}

func TestRehearseFilePullRefusals(t *testing.T) {
	_, opts := fileTest(t)
	c, _, _ := fileChallenge(t)
	push := c
	push.Mode, push.ProofPath, push.ProofKey = rehearse.ConfigPush, "", "user.spire.challenge.0123456789abcdef"
	for name, tt := range map[string]struct {
		allowSpire bool
		challenge  string
		code       int
	}{
		"SPIRE path without --allow-spire-paths": {false, challengeLine(t, c), exit.Refused},
		"a config_push challenge":                {true, challengeLine(t, push), exit.Usage},
		"not a challenge":                        {true, "{}", exit.Usage},
	} {
		o := opts
		o.AllowSpirePaths = tt.allowSpire
		cmd := &RehearseFilePull{GuestCommand: o, Challenge: tt.challenge, stdout: &bytes.Buffer{}}
		if err := cmd.Execute(nil); exit.Code(err) != tt.code {
			t.Errorf("%s: exit %d (%v), want %d", name, exit.Code(err), err, tt.code)
		}
	}
}

// tamperName always changes the name and keeps it valid.
func TestTamperName(t *testing.T) {
	for _, name := range []string{"c1", "a", strings.Repeat("x", 63), strings.Repeat("a", 63)} {
		got := tamperName(name)
		if got == name || len(got) != len(name) || got[len(got)-1] == '-' {
			t.Errorf("tamperName(%q) = %q", name, got)
		}
	}
}

func pushChallenge() rehearse.Challenge {
	return rehearse.Challenge{
		Mode: rehearse.ConfigPush, Project: "default", Instance: "v-ubuntu",
		ServerNonce: strings.Repeat("0a", 32), AgentNonce: strings.Repeat("0b", 32),
		ProofKey: "user.spire.challenge.0123456789abcdef", TimeoutMS: 30000,
	}
}

// TestPRF25RehearseConfigPush: the guest reads the challenge key through
// devLXD and echoes it; --tamper changes one character.
func TestPRF25RehearseConfigPush(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	value := strings.Repeat("0a", 32)
	fake.SetConfig("user.spire.challenge.0123456789abcdef", value)
	for _, tamper := range []bool{false, true} {
		out := &bytes.Buffer{}
		cmd := &RehearseConfigPush{GuestCommand: guestOptions(t, fake.Socket), Challenge: challengeLine(t, pushChallenge()), Tamper: tamper, stdout: out}
		if err := cmd.Execute(nil); err != nil {
			t.Fatal(err)
		}
		a, err := rehearse.ReadAnswer(context.Background(), bytes.NewReader(out.Bytes()))
		if err != nil || !a.Done || a.Mode != rehearse.ConfigPush || (a.Echo == value) == tamper || !proof.IsHexProof([]byte(a.Echo)) {
			t.Errorf("tamper %v: answer %+v, %v", tamper, a, err)
		}
	}
}

func TestRehearseConfigPushFailures(t *testing.T) {
	fake := fakelxd.NewDevLXD(t)
	fake.SetConfig("user.spire.challenge.0123456789abcdef", "not a proof")
	for name, tt := range map[string]struct {
		challenge rehearse.Challenge
		reason    string
	}{
		"value is not a hex proof": {pushChallenge(), "not 64 lowercase hex"},
		"key absent": {func() rehearse.Challenge {
			c := pushChallenge()
			c.ProofKey = "user.spire.challenge.ffffffffffffffff"
			return c
		}(), "status 404"},
	} {
		out := &bytes.Buffer{}
		cmd := &RehearseConfigPush{GuestCommand: guestOptions(t, fake.Socket), Challenge: challengeLine(t, tt.challenge), stdout: out}
		_ = cmd.Execute(nil)
		a, err := rehearse.ReadAnswer(context.Background(), bytes.NewReader(out.Bytes()))
		if err != nil || a.Done || !strings.Contains(a.Error, tt.reason) {
			t.Errorf("%s: answer %+v, %v", name, a, err)
		}
	}
	c, _, _ := fileChallenge(t)
	cmd := &RehearseConfigPush{GuestCommand: guestOptions(t, fake.Socket), Challenge: challengeLine(t, c), stdout: &bytes.Buffer{}}
	if err := cmd.Execute(nil); exit.Code(err) != exit.Usage {
		t.Errorf("a file_pull challenge: exit %d (%v)", exit.Code(err), err)
	}
}

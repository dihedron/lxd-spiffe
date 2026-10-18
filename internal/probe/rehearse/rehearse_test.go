package rehearse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func validChallenge() Challenge {
	return Challenge{
		Mode: FilePull, Project: "default", Instance: "c-unpriv",
		ServerNonce: strings.Repeat("00", 32), AgentNonce: strings.Repeat("01", 32),
		ProofPath: "/run/spire/lxd/proof-0123456789abcdef", TimeoutMS: 30000,
	}
}

// TestPRS53RoundTrip: the challenge and the answer travel as one tagged
// JSON line each, found among other output.
func TestPRS53RoundTrip(t *testing.T) {
	var out bytes.Buffer
	out.WriteString("step resolve: ok\n")
	if err := WriteChallenge(&out, validChallenge()); err != nil {
		t.Fatal(err)
	}
	out.WriteString("step challenge: ok\n")
	c, err := FindChallenge(out.String())
	if err != nil || c != validChallenge() {
		t.Fatalf("challenge %+v, %v", c, err)
	}
	var ans bytes.Buffer
	ans.WriteString("record 0001 ok\n")
	if err := WriteAnswer(&ans, Answer{Mode: FilePull, Done: true}); err != nil {
		t.Fatal(err)
	}
	a, err := ReadAnswer(context.Background(), &ans)
	if err != nil || a != (Answer{Mode: FilePull, Done: true}) {
		t.Fatalf("answer %+v, %v", a, err)
	}
}

func TestParseChallenge(t *testing.T) {
	var line bytes.Buffer
	_ = WriteChallenge(&line, validChallenge())
	bare := `{"mode":"config_push","project":"default","instance":"v-ubuntu","server_nonce":"` + strings.Repeat("ab", 32) +
		`","agent_nonce":"` + strings.Repeat("cd", 32) + `","proof_key":"user.spire.challenge.0123456789abcdef","timeout_ms":5000}`
	for name, text := range map[string]string{"tagged line": line.String(), "bare object": bare} {
		if _, err := ParseChallenge(text); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*Challenge){
		"unknown mode":         func(c *Challenge) { c.Mode = "ready_signal" },
		"short server nonce":   func(c *Challenge) { c.ServerNonce = "00" },
		"agent nonce not hex":  func(c *Challenge) { c.AgentNonce = strings.Repeat("zz", 32) },
		"bad instance name":    func(c *Challenge) { c.Instance = "c_1" },
		"file_pull no path":    func(c *Challenge) { c.ProofPath = "" },
		"file_pull with a key": func(c *Challenge) { c.ProofKey = "user.spire.challenge.0123456789abcdef" },
		"config_push bad key":  func(c *Challenge) { c.Mode, c.ProofPath, c.ProofKey = ConfigPush, "", "user.other" },
		"no timeout":           func(c *Challenge) { c.TimeoutMS = 0 },
	} {
		c := validChallenge()
		mutate(&c)
		var b bytes.Buffer
		_ = WriteChallenge(&b, c)
		if _, err := ParseChallenge(b.String()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := FindChallenge("no challenge here\n"); err == nil {
		t.Error("FindChallenge without a challenge")
	}
}

// blockingReader never returns, like a guest that never answers.
type blockingReader struct{ done chan struct{} }

func (b blockingReader) Read([]byte) (int, error) { <-b.done; return 0, io.EOF }

func TestReadAnswerTimeout(t *testing.T) {
	r := blockingReader{done: make(chan struct{})}
	defer close(r.done)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := ReadAnswer(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v", err)
	}
	if _, err := ReadAnswer(context.Background(), strings.NewReader("nothing\n")); err == nil {
		t.Error("EOF without an answer accepted")
	}
}

func TestGuestCommand(t *testing.T) {
	got := GuestCommand(validChallenge())
	if !strings.HasPrefix(got, "lxd-probe guest rehearse file-pull --allow-write --lab --allow-spire-paths --challenge '{") {
		t.Errorf("%s", got)
	}
	c := validChallenge()
	c.Mode, c.ProofPath, c.ProofKey = ConfigPush, "", "user.spire.challenge.0123456789abcdef"
	if got := GuestCommand(c); !strings.HasPrefix(got, "lxd-probe guest rehearse config-push --challenge '{") {
		t.Errorf("%s", got)
	}
}

// TestGuestCommandQuoting: the printed command survives a shell whatever
// the challenge holds, single quotes included (validation would refuse
// them, but the command must not rely on it).
func TestGuestCommandQuoting(t *testing.T) {
	c := validChallenge()
	c.Instance = `it's "odd" $(touch /tmp/x) \`
	data, _ := json.Marshal(c)
	out, err := exec.Command("sh", "-c", `eval "set -- $1"; for a; do last=$a; done; printf '%s' "$last"`, "sh", GuestCommand(c)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(data) {
		t.Errorf("the shell saw\n%s\nnot\n%s", out, data)
	}
}

// Package rehearse is the exchange between the server and the guest halves
// of a rehearsal (probe spec PRS-53): the challenge and the answer, each one
// tagged JSON line, in place of the SPIRE stream.
package rehearse

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/limits"
	"github.com/dihedron/lxd-spiffe/internal/proof"
)

// The proof modes, as the attestor names them.
const (
	FilePull   = "file_pull"
	ConfigPush = "config_push"
)

// The tags that mark the lines among a command's other output.
const (
	challengeTag = "lxd_probe_challenge"
	answerTag    = "lxd_probe_answer"
)

// Challenge is the server's challenge: what the attestor's Challenge
// carries, plus what the agent would know from its own claim.
type Challenge struct {
	Mode        string `json:"mode"`
	Project     string `json:"project"`
	Instance    string `json:"instance"`
	ServerNonce string `json:"server_nonce"` // hex
	AgentNonce  string `json:"agent_nonce"`  // hex
	ProofPath   string `json:"proof_path,omitempty"`
	ProofKey    string `json:"proof_key,omitempty"`
	TimeoutMS   int64  `json:"timeout_ms"`
}

// Answer is the guest's answer: done for file_pull, the echo for
// config_push; Error says why the guest step failed.
type Answer struct {
	Mode  string `json:"mode"`
	Done  bool   `json:"done"`
	Echo  string `json:"echo,omitempty"`
	Error string `json:"error,omitempty"`
}

// Nonces returns the decoded server and agent nonces.
func (c Challenge) Nonces() (server, agent []byte, err error) {
	if server, err = decodeNonce("server", c.ServerNonce); err != nil {
		return nil, nil, err
	}
	if agent, err = decodeNonce("agent", c.AgentNonce); err != nil {
		return nil, nil, err
	}
	return server, agent, nil
}

func decodeNonce(which, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != limits.NonceSize {
		return nil, fmt.Errorf("%s nonce is not %d bytes of hex", which, limits.NonceSize)
	}
	return b, nil
}

// Validate checks a challenge before the guest acts on it.
func (c Challenge) Validate() error {
	if _, _, err := c.Nonces(); err != nil {
		return err
	}
	if err := claim.ValidInstanceName(c.Instance); err != nil {
		return err
	}
	if c.Project != "" {
		if err := claim.ValidProject(c.Project); err != nil {
			return err
		}
	}
	if c.TimeoutMS <= 0 {
		return errors.New("challenge without a timeout")
	}
	switch c.Mode {
	case FilePull:
		if c.ProofKey != "" || !path.IsAbs(c.ProofPath) || path.Clean(c.ProofPath) != c.ProofPath {
			return fmt.Errorf("file_pull challenge: proof path %q, key %q", c.ProofPath, c.ProofKey)
		}
	case ConfigPush:
		if c.ProofPath != "" || !proof.IsKey(c.ProofKey) {
			return fmt.Errorf("config_push challenge: proof path %q, key %q", c.ProofPath, c.ProofKey)
		}
	default:
		return fmt.Errorf("unknown mode %q", c.Mode)
	}
	return nil
}

// WriteChallenge writes the challenge's tagged line.
func WriteChallenge(w io.Writer, c Challenge) error {
	return writeTagged(w, challengeTag, c)
}

// WriteAnswer writes the answer's tagged line.
func WriteAnswer(w io.Writer, a Answer) error {
	return writeTagged(w, answerTag, a)
}

func writeTagged(w io.Writer, tag string, v any) error {
	data, err := json.Marshal(map[string]any{tag: v})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// ParseChallenge parses a challenge given as its tagged line or as the bare
// object, as an operator may paste either, and validates it.
func ParseChallenge(text string) (Challenge, error) {
	text = strings.TrimSpace(text)
	var tagged map[string]json.RawMessage
	raw := json.RawMessage(text)
	if json.Unmarshal(raw, &tagged) == nil {
		if inner, ok := tagged[challengeTag]; ok {
			raw = inner
		}
	}
	var c Challenge
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Challenge{}, fmt.Errorf("challenge: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Challenge{}, fmt.Errorf("challenge: %w", err)
	}
	return c, nil
}

// FindChallenge returns the challenge of the first tagged line in output.
func FindChallenge(output string) (Challenge, error) {
	for line := range strings.Lines(output) {
		if strings.Contains(line, `"`+challengeTag+`"`) {
			return ParseChallenge(line)
		}
	}
	return Challenge{}, errors.New("no challenge in the output")
}

// ReadAnswer reads lines from r until the answer's tagged line, the end of
// r, or the end of ctx.
func ReadAnswer(ctx context.Context, r io.Reader) (Answer, error) {
	type result struct {
		a   Answer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(io.LimitReader(r, limits.MaxLXDResponse))
		for sc.Scan() {
			var tagged map[string]json.RawMessage
			if json.Unmarshal(sc.Bytes(), &tagged) != nil {
				continue
			}
			if inner, ok := tagged[answerTag]; ok {
				var a Answer
				err := json.Unmarshal(inner, &a)
				ch <- result{a, err}
				return
			}
		}
		ch <- result{err: errors.New("no answer before the end of input")}
	}()
	select {
	case res := <-ch:
		return res.a, res.err
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// GuestCommand is the guest command that answers c, for the operator.
func GuestCommand(c Challenge) string {
	data, _ := json.Marshal(c)
	flags := ""
	verb := "config-push"
	if c.Mode == FilePull {
		verb, flags = "file-pull", " --allow-write --lab --allow-spire-paths"
	}
	return fmt.Sprintf("lxd-probe guest rehearse %s%s --challenge %s", verb, flags, shellQuote(string(data)))
}

// shellQuote quotes s for a POSIX shell: single quotes, each embedded one
// written as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

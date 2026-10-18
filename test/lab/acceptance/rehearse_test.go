//go:build lab

package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
)

// rehearsal is one rehearsal's two halves.
type rehearsal struct {
	server    *run
	guest     *run
	challenge rehearse.Challenge
}

// rehearse runs a rehearsal (probe spec PRS-53): the server half through
// lab.sh probe with its standard streams piped, the guest half through
// lab.sh guest with the challenge, and the guest's answer fed back.
// serverArgs follow "server rehearse MODE INSTANCE", guestArgs "guest
// rehearse MODE".
func (l *lab) rehearse(t *testing.T, s session, server, identity, project, instance, mode string, serverArgs, guestArgs []string) rehearsal {
	t.Helper()
	reh, err := l.rehearseE(context.Background(), s, server, identity, project, instance, mode, serverArgs, guestArgs)
	if err != nil {
		t.Fatal(err)
	}
	return reh
}

// rehearseE is rehearse returning its failure, for the stress workers;
// ctx kills both halves.
func (l *lab) rehearseE(ctx context.Context, s session, server, identity, project, instance, mode string, serverArgs, guestArgs []string) (rehearsal, error) {
	var reh rehearsal
	verb := map[string]string{rehearse.FilePull: "file-pull", rehearse.ConfigPush: "config-push"}[mode]
	args := append([]string{"probe", server, "--as", identity, "--", "server", "rehearse", verb, instance, "--project", project}, serverArgs...)
	args = append(args, "--json", "--evidence-dir", s.evidenceDir)
	cmd := exec.CommandContext(ctx, filepath.Join(l.dir, "lab.sh"), args...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return reh, err
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return reh, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return reh, fmt.Errorf("lab.sh %s: %w", strings.Join(args, " "), err)
	}
	// the challenge line comes first; everything is kept for the records
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pipe)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			stdout.WriteString(sc.Text() + "\n")
			lines <- sc.Text()
		}
	}()
	var line string
	deadline := time.After(2 * time.Minute)
wait:
	for {
		select {
		case ln, ok := <-lines:
			if !ok {
				break wait
			}
			if c, err := rehearse.FindChallenge(ln); err == nil {
				reh.challenge, line = c, ln
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	var guestErr error
	if line != "" {
		gargs := append([]string{"guest", "rehearse", verb}, guestArgs...)
		gargs = append(gargs, "--challenge", line)
		reh.guest, guestErr = l.guestE(ctx, s, server, project, instance, gargs...)
		if reh.guest != nil {
			actx, cancel := context.WithTimeout(ctx, 5*time.Second)
			answer, err := rehearse.ReadAnswer(actx, strings.NewReader(reh.guest.stdout))
			cancel()
			if err == nil {
				_ = rehearse.WriteAnswer(stdin, answer)
			}
		}
	}
	stdin.Close() //nolint:errcheck // the server half reads EOF without an answer
	for range lines {
	}
	err = cmd.Wait()
	reh.server, err = parseRunE(args, err, stdout.String(), stderr.String())
	switch {
	case err != nil:
		return reh, err
	case line == "":
		return reh, fmt.Errorf("%s %s/%s: no challenge: %s", server, project, instance, reh.server)
	case guestErr != nil && reh.guest == nil:
		return reh, guestErr
	}
	return reh, nil
}

// result is a rehearsal record's result observation.
func (r *run) result() string {
	v, _ := r.observation("rehearsal.result")
	s, _ := v.(string)
	return s
}

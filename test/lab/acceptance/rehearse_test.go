//go:build lab

package acceptance

import (
	"bufio"
	"bytes"
	"context"
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
	verb := map[string]string{rehearse.FilePull: "file-pull", rehearse.ConfigPush: "config-push"}[mode]
	args := append([]string{"probe", server, "--as", identity, "--", "server", "rehearse", verb, instance, "--project", project}, serverArgs...)
	args = append(args, "--json", "--evidence-dir", s.evidenceDir)
	cmd := exec.Command(filepath.Join(l.dir, "lab.sh"), args...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("lab.sh %s: %v", strings.Join(args, " "), err)
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
	var reh rehearsal
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
	if line != "" {
		gargs := append([]string{"guest", "rehearse", verb}, guestArgs...)
		gargs = append(gargs, "--challenge", line)
		reh.guest = l.guest(t, s, server, project, instance, gargs...)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answer, err := rehearse.ReadAnswer(ctx, strings.NewReader(reh.guest.stdout))
		cancel()
		if err == nil {
			_ = rehearse.WriteAnswer(stdin, answer)
		}
	}
	stdin.Close() //nolint:errcheck // the server half reads EOF without an answer
	for range lines {
	}
	err = cmd.Wait()
	reh.server = l.parseRun(t, args, err, stdout.String(), stderr.String())
	if line == "" {
		t.Fatalf("%s %s/%s: no challenge: %s", server, project, instance, reh.server)
	}
	return reh
}

// result is a rehearsal record's result observation.
func (r *run) result() string {
	v, _ := r.observation("rehearsal.result")
	s, _ := v.(string)
	return s
}

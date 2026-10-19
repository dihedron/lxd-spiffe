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
	"syscall"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
)

// rehearsal is one rehearsal's two halves.
type rehearsal struct {
	server    *run
	guest     *run
	challenge rehearse.Challenge
	// guestErr is why the guest half failed when the server half's record
	// still says how the rehearsal ended: the record judges it
	guestErr error
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
	return l.rehearseHooked(ctx, s, server, identity, project, instance, mode, serverArgs, guestArgs, rehearseHooks{})
}

// rehearseHooks are the points where the chaos sessions inject faults
// (stress spec, Fault catalogue).
type rehearseHooks struct {
	// afterChallenge runs once the server half has sent its challenge
	// (config_push: after its write), before the guest half.
	afterChallenge func()
	// skipGuest runs no guest half: the server half gets no answer (C5).
	skipGuest bool
	// afterGuest runs after the guest half, before its answer is fed back.
	afterGuest func()
	// killServer kills the server half with SIGKILL right after the
	// challenge, before afterChallenge (C4); the guest half still runs.
	killServer bool
}

// rehearseHooked is rehearseE with fault injection points.
func (l *lab) rehearseHooked(ctx context.Context, s session, server, identity, project, instance, mode string, serverArgs, guestArgs []string, h rehearseHooks) (rehearsal, error) {
	var reh rehearsal
	verb := map[string]string{rehearse.FilePull: "file-pull", rehearse.ConfigPush: "config-push"}[mode]
	args := append([]string{"probe", server, "--as", identity, "--", "server", "rehearse", verb, instance, "--project", project}, serverArgs...)
	args = append(args, "--json", "--evidence-dir", s.evidenceDir)
	cmd := exec.CommandContext(ctx, filepath.Join(l.dir, "lab.sh"), args...)
	cmd.Env = append(os.Environ(), "LXD_PROBE_RUN_ID="+s.runID)
	// lab.sh runs lxd-probe in a subshell: a process group of its own lets
	// C4 kill the probe itself, not only lab.sh's shell
	boundCommand(cmd)
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
	var received time.Time
	deadline := time.After(2 * time.Minute)
wait:
	for {
		select {
		case ln, ok := <-lines:
			if !ok {
				break wait
			}
			if c, err := rehearse.FindChallenge(ln); err == nil {
				reh.challenge, line, received = c, ln, time.Now()
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	var guestErr error
	if line != "" && h.killServer {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the whole group: lab.sh and lxd-probe
	}
	if line != "" && h.afterChallenge != nil {
		h.afterChallenge()
	}
	if line != "" && !h.skipGuest {
		gargs := append([]string{"guest", "rehearse", verb}, guestArgs...)
		gargs = append(gargs, "--challenge", line)
		// the guest half ends when the server half stops waiting for it: an
		// lxc exec into an instance a power-off or an evacuation took away
		// never ends, and waiting for it to the iteration's end lost the
		// server half's record, which had ended long before (ST-4)
		gctx, gcancel := ctx, context.CancelFunc(func() {})
		if d := guestDeadline(received, reh.challenge); !d.IsZero() {
			gctx, gcancel = context.WithDeadline(ctx, d)
		}
		reh.guest, guestErr = l.guestE(gctx, s, server, project, instance, gargs...)
		gcancel()
		if h.afterGuest != nil {
			h.afterGuest()
		}
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
	case line == "" && reh.server.result() == "":
		return reh, fmt.Errorf("%s %s/%s: no challenge: %s", server, project, instance, reh.server)
	case guestErr != nil && reh.guest == nil && reh.server.result() == "":
		return reh, guestErr
	}
	if reh.guest == nil {
		reh.guestErr = guestErr
	}
	return reh, nil
}

// result is a rehearsal record's result observation.
func (r *run) result() string {
	v, _ := r.observation("rehearsal.result")
	s, _ := v.(string)
	return s
}

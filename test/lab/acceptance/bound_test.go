package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe/rehearse"
)

// boundCommand makes the end of cmd's context end cmd for good: lab.sh
// runs in a process group of its own, killed as a whole, and Wait gives
// up on output pipes a leftover child still holds after a few seconds.
// Without it, an ssh that outlived lab.sh kept an iteration, and the
// stress session behind it, waiting for 40 minutes (ST-4).
func boundCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

// TestBoundCommand: a shell whose child holds stdout open, the way ssh
// outlived lab.sh, ends with its context, child included.
func TestBoundCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & echo $!; wait")
	boundCommand(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("the command outlived its context")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("Run returned after %s", d)
	}
	var child int
	if _, err := fmt.Sscan(out.String(), &child); err != nil || child == 0 {
		t.Fatalf("no child pid in %q", out.String())
	}
	// killed, it is a zombie until init reaps it
	gone := func() bool {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		return err != nil || strings.Contains(string(stat), ") Z ")
	}
	for end := time.Now().Add(2 * time.Second); !gone() && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
	if !gone() {
		syscall.Kill(child, syscall.SIGKILL) //nolint:errcheck // cleanup
		t.Errorf("the child %d survived", child)
	}
}

// guestMargin is how long the guest half may outlast the server half's
// --timeout: its answer is of no use once the server half gave up.
const guestMargin = 5 * time.Second

// guestDeadline is when the guest half must end: the challenge's timeout
// after it was received, plus guestMargin; zero when the challenge has no
// timeout, and the iteration's context alone bounds the guest half.
func guestDeadline(received time.Time, c rehearse.Challenge) time.Time {
	if c.TimeoutMS <= 0 {
		return time.Time{}
	}
	return received.Add(time.Duration(c.TimeoutMS)*time.Millisecond + guestMargin)
}

// TestGuestDeadline: the guest half ends guestMargin after the server
// half's timeout, counted from the challenge; without a timeout, only the
// iteration's context bounds it.
func TestGuestDeadline(t *testing.T) {
	at := time.Date(2026, 10, 19, 8, 10, 27, 0, time.UTC)
	if got, want := guestDeadline(at, rehearse.Challenge{TimeoutMS: 30000}), at.Add(35*time.Second); !got.Equal(want) {
		t.Errorf("30 s timeout: deadline %s, want %s", got, want)
	}
	for _, ms := range []int64{0, -1} {
		if got := guestDeadline(at, rehearse.Challenge{TimeoutMS: ms}); !got.IsZero() {
			t.Errorf("timeout %d ms: deadline %s, want none", ms, got)
		}
	}
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// boundedRemote bounds a command run on a lab VM there, as root: the
// local end of an ssh killed does not end an lxc on the member, and an
// lxc stop was seen waiting 12 minutes for a VM already stopped (ST-4).
// After d, SIGTERM; 5 s later, SIGKILL, which lxc exec cannot pass on.
func boundedRemote(d time.Duration, command string) string {
	return fmt.Sprintf("sudo timeout -k 5 %d sh -c %s", int(d.Seconds()), shellQuote(command))
}

// TestBoundedRemote: the command is quoted whole, bounded in seconds,
// and runs unchanged.
func TestBoundedRemote(t *testing.T) {
	got := boundedRemote(2*time.Minute, `sudo lxc list -c L -f csv "^s-001$" | grep -v 'x'`)
	want := `sudo timeout -k 5 120 sh -c 'sudo lxc list -c L -f csv "^s-001$" | grep -v '\''x'\'''`
	if got != want {
		t.Errorf("boundedRemote:\n got %s\nwant %s", got, want)
	}
	cmd := strings.TrimPrefix(boundedRemote(time.Second, `echo "it's" $((1+1))`), "sudo ")
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "it's 2" {
		t.Errorf("%s ran as %q, %v", cmd, out, err)
	}
}

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

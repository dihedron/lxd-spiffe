package startup

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/pkg/metadata"
)

// keepLogger restores the default logger after a test that calls Init.
func keepLogger(t *testing.T) {
	t.Helper()
	saved := slog.Default()
	t.Cleanup(func() { slog.SetDefault(saved) })
}

// TestLogLevel: the values of the level variable, and the default, which
// logs nothing.
func TestLogLevel(t *testing.T) {
	keepLogger(t)
	for _, tc := range []struct {
		value string
		on    slog.Level
		off   slog.Level
	}{
		{"debug", slog.LevelDebug, slog.LevelDebug - 1},
		{"info", slog.LevelInfo, slog.LevelDebug},
		{"warn", slog.LevelWarn, slog.LevelInfo},
		{"error", slog.LevelError, slog.LevelWarn},
		{"off", slog.Level(1000), slog.LevelError},
		{"", slog.Level(1000), slog.LevelError},
	} {
		if tc.value != "" {
			t.Setenv("STARTUP_TEST_LOG_LEVEL", tc.value)
		} else {
			os.Unsetenv("STARTUP_TEST_LOG_LEVEL") //nolint:errcheck // a test's environment
		}
		Init("startup-test")
		h := slog.Default().Handler()
		if !h.Enabled(context.Background(), tc.on) || h.Enabled(context.Background(), tc.off) {
			t.Errorf("%q: enabled at %v %v, at %v %v", tc.value, tc.on, h.Enabled(context.Background(), tc.on), tc.off, h.Enabled(context.Background(), tc.off))
		}
	}
}

// TestLogStream: the log goes to a file named after the binary and its
// process ID when asked; standard error and output otherwise.
func TestLogStream(t *testing.T) {
	keepLogger(t)
	t.Chdir(t.TempDir())
	t.Setenv("STARTUP_TEST_LOG_LEVEL", "info")
	for _, v := range []string{"stdout", "stderr"} {
		t.Setenv("STARTUP_TEST_LOG_STREAM", v)
		Init("startup-test")
	}
	t.Setenv("STARTUP_TEST_LOG_STREAM", "file")
	Init("startup-test")
	slog.Info("to the file")
	files, _ := filepath.Glob("startup-test-*.log")
	if len(files) != 1 {
		t.Fatalf("log files %v", files)
	}
	if data, _ := os.ReadFile(files[0]); !strings.Contains(string(data), "to the file") {
		t.Errorf("log file holds %q", data)
	}
}

// TestDotEnv: the file the variable names is loaded before the variables are
// read; a failure is logged at the configured level, or written to standard
// error when no level is set, so that it is never lost.
func TestDotEnv(t *testing.T) {
	keepLogger(t)
	saved := metadata.DotEnvVarName
	metadata.DotEnvVarName = "STARTUP_TEST_DOTENV"
	t.Cleanup(func() { metadata.DotEnvVarName = saved })
	dir := t.TempDir()
	good := filepath.Join(dir, ".env")
	if err := os.WriteFile(good, []byte("STARTUP_TEST_FROM_DOTENV=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("STARTUP_TEST_FROM_DOTENV") }) //nolint:errcheck // a test's environment
	t.Setenv("STARTUP_TEST_DOTENV", good)
	t.Setenv("STARTUP_TEST_LOG_LEVEL", "info")
	Init("startup-test")
	if os.Getenv("STARTUP_TEST_FROM_DOTENV") != "yes" {
		t.Error("the .env file was not loaded")
	}
	t.Setenv("STARTUP_TEST_DOTENV", filepath.Join(dir, "missing"))
	Init("startup-test")                  // logged, at error level
	os.Unsetenv("STARTUP_TEST_LOG_LEVEL") //nolint:errcheck // a test's environment
	stderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	Init("startup-test")
	os.Stderr = stderr
	w.Close() //nolint:errcheck // the write end of a test pipe
	var out bytes.Buffer
	io.Copy(&out, r) //nolint:errcheck // a test pipe
	if !strings.Contains(out.String(), "error loading .env file") {
		t.Errorf("standard error %q", out.String())
	}
}

func TestCleanupProfiles(t *testing.T) {
	dir := t.TempDir()
	var err error
	if cpuprof, err = os.Create(dir + "/cpu"); err != nil {
		t.Fatal(err)
	}
	if memprof, err = os.Create(dir + "/mem"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cpuprof, memprof = nil, nil })
	Cleanup()
	if info, err := os.Stat(dir + "/mem"); err != nil || info.Size() == 0 {
		t.Errorf("memory profile not written: %v", err)
	}
}

// TestProfiles: profiles go to the files the variables name, and are
// written by Cleanup.
func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STARTUP_TEST_CPU_PROFILE", filepath.Join(dir, "cpu"))
	t.Setenv("STARTUP_TEST_MEM_PROFILE", filepath.Join(dir, "mem"))
	Init("startup-test")
	t.Cleanup(func() { cpuprof, memprof = nil, nil })
	Cleanup()
	for _, f := range []string{"cpu", "mem"} {
		if info, err := os.Stat(filepath.Join(dir, f)); err != nil || info.Size() == 0 {
			t.Errorf("%s profile not written: %v", f, err)
		}
	}
}

// TestProfileNotCreated: a profile file that cannot be created is skipped:
// no profiling starts, and Cleanup has nothing to do (PRN-30).
func TestProfileNotCreated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "profile")
	t.Setenv("STARTUP_TEST_CPU_PROFILE", missing)
	t.Setenv("STARTUP_TEST_MEM_PROFILE", missing)
	Init("startup-test")
	t.Cleanup(func() { cpuprof, memprof = nil, nil })
	if cpuprof != nil || memprof != nil {
		t.Errorf("profile files %v, %v", cpuprof, memprof)
	}
	if err := pprof.StartCPUProfile(io.Discard); err != nil {
		t.Errorf("CPU profiling was started without a file: %v", err)
	} else {
		pprof.StopCPUProfile()
	}
	Cleanup()
}

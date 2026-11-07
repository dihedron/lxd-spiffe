package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dotEnvVar = "LXD_SPIFFE_DOTENV"

// built returns an lxd-probe binary: init() reads variables named after the
// binary, and DotEnvVarName is stamped at build time.
func built(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lxd-probe")
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/dihedron/lxd-spiffe/pkg/metadata.DotEnvVarName="+dotEnvVar, "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// probe runs the binary in dir with only env set, and returns its exit code
// and combined output.
func probe(t *testing.T, bin, dir string, env []string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("%v: hung\n%s", args, out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	case err != nil:
		t.Fatal(err)
	}
	return 0, string(out)
}

func writeEnv(t *testing.T, dir, content string) string {
	t.Helper()
	file := filepath.Join(dir, ".env")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestNoDotEnvFromWorkingDirectory: a .env in the working directory, which
// may not be the operator's, must not set the probe's options: it could send
// the bearer token to an endpoint of its choice.
func TestNoDotEnvFromWorkingDirectory(t *testing.T) {
	bin := built(t)
	dir := t.TempDir()
	writeEnv(t, dir, "LXD_PROBE_ENDPOINT=https://planted.invalid:8443\nLXD_PROBE_PIN="+strings.Repeat("a", 64)+"\n")
	if _, out := probe(t, bin, dir, nil, "server", "info"); strings.Contains(out, "planted.invalid") {
		t.Errorf("the working directory's .env was loaded: %s", out)
	}
	// and its absence is no event
	if _, out := probe(t, bin, t.TempDir(), []string{"LXD_PROBE_LOG_LEVEL=warn"}, "version"); strings.Contains(out, ".env") {
		t.Errorf("a warning about a missing .env: %s", out)
	}
}

// TestDotEnvLogging: the file named by the variable is loaded, its lines
// follow the configured level, and a failure is never lost by default.
func TestDotEnvLogging(t *testing.T) {
	bin := built(t)
	good := writeEnv(t, t.TempDir(), "X=1\n")
	missing := filepath.Join(t.TempDir(), "missing")
	const loaded, failed = "successfully loaded", "error loading"
	for _, tc := range []struct {
		name, file, level, want string
		silent                  bool
	}{
		{name: "off, good", file: good, level: "off", silent: true},
		{name: "off, missing", file: missing, level: "off", silent: true},
		{name: "unset, good", file: good, silent: true},
		{name: "error, good", file: good, level: "error", silent: true},
		{name: "info, good", file: good, level: "info", want: loaded},
		{name: "error, missing", file: missing, level: "error", want: failed},
		{name: "unset, missing", file: missing, want: failed},
		{name: "none, good", file: good, level: "none", silent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{dotEnvVar + "=" + tc.file}
			if tc.level != "" {
				env = append(env, "LXD_PROBE_LOG_LEVEL="+tc.level)
			}
			code, out := probe(t, bin, t.TempDir(), env, "util", "--help")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			if tc.silent && (strings.Contains(out, loaded) || strings.Contains(out, failed)) {
				t.Errorf("output: %s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output %q does not contain %q", out, tc.want)
			}
		})
	}
}

// TestMalformedDotEnvIsNotQuoted: godotenv's parse error quotes the file from
// the bad line on, and later lines may hold secrets.
func TestMalformedDotEnvIsNotQuoted(t *testing.T) {
	bin := built(t)
	const canary, badLine = "canary-value", "bad line here"
	bad := writeEnv(t, t.TempDir(), badLine+"\nSECRET="+canary+"\n")
	for _, env := range [][]string{{dotEnvVar + "=" + bad}, {dotEnvVar + "=" + bad, "LXD_PROBE_LOG_LEVEL=error"}} {
		_, out := probe(t, bin, t.TempDir(), env, "util", "--help")
		if strings.Contains(out, canary) || strings.Contains(out, badLine) {
			t.Errorf("the file is quoted: %s", out)
		}
		if !strings.Contains(out, "malformed") {
			t.Errorf("no report of the failure: %s", out)
		}
	}
}

// Package startup is what the three binaries do before main (lxd-probe spec
// PRN-30): the log/slog set-up, the .env file that DotEnvVarName names, and
// CPU and memory profiling, all from variables named after the binary.
package startup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/dihedron/lxd-spiffe/pkg/metadata"
)

// levelNone logs nothing: the level when none is set.
const levelNone = slog.Level(1000)

var (
	cpuprof *os.File
	memprof *os.File
)

// debug starts the debugging aids of a lab build (debug_lab.go); release
// builds have none.
var debug = func(string) {}

// Init sets up logging and profiling for the binary name (the base name of
// os.Args[0]); the variables it reads are named after it: for "my-app",
// MY_APP_LOG_LEVEL, MY_APP_LOG_STREAM, MY_APP_CPU_PROFILE and
// MY_APP_MEM_PROFILE.
func Init(name string) {
	// load .env file if specified and present; it may set the variables below,
	// so it comes first, and is logged once the logger is configured
	dotenv, dotenvSet := os.LookupEnv(metadata.DotEnvVarName)
	var dotenvErr error
	if dotenvSet {
		dotenvErr = metadata.LoadDotEnv(dotenv)
	}

	level, levelSet := os.LookupEnv(variable(name, "LOG_LEVEL"))
	options := &slog.HandlerOptions{Level: levelNone, AddSource: true}
	if levelSet {
		options.Level = parseLevel(level)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter(name), options)))

	if dotenvSet {
		switch {
		case dotenvErr == nil:
			slog.Info("successfully loaded .env file", "path", dotenv)
		case levelSet:
			slog.Error("error loading .env file", "path", dotenv, "error", dotenvErr)
		default: // no level set means no logger, but a failed load must not go unseen
			fmt.Fprintf(os.Stderr, "error loading .env file %s: %v\n", dotenv, dotenvErr)
		}
	}

	startProfiles(name)
	debug(name)
}

// variable is the name of the binary's variable: for "my-app" and
// "LOG_LEVEL", MY_APP_LOG_LEVEL.
func variable(name, suffix string) string {
	return strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_" + suffix
}

// parseLevel reads a log level; an unknown value logs nothing.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug", "dbg", "d", "trace", "trc", "t":
		return slog.LevelDebug
	case "informational", "info", "inf", "i":
		return slog.LevelInfo
	case "warning", "warn", "wrn", "w":
		return slog.LevelWarn
	case "error", "err", "e", "fatal", "ftl", "f":
		return slog.LevelError
	}
	return levelNone
}

// logWriter is where the log goes: standard error unless the stream variable
// asks for standard output or a file named after the binary and its process.
func logWriter(name string) io.Writer {
	stream, _ := os.LookupEnv(variable(name, "LOG_STREAM"))
	switch strings.ToLower(stream) {
	case "stdout", "output", "out", "o":
		return os.Stdout
	case "file":
		if f, err := os.Create(path.Clean(fmt.Sprintf("%s-%d.log", name, os.Getpid()))); err == nil {
			return f
		}
	}
	return os.Stderr
}

// startProfiles starts CPU profiling and opens the memory profile when the
// variables name their files; a file that cannot be created is logged and
// skipped (PRN-30).
func startProfiles(name string) {
	if filename := os.Getenv(variable(name, "CPU_PROFILE")); filename != "" {
		f, err := os.Create(path.Clean(filename)) //nolint:gosec // G703: the operator names the profile file in the environment
		switch {
		case err != nil:
			slog.Error("could not create CPU profile", "error", err)
		case pprof.StartCPUProfile(f) != nil:
			slog.Error("could not start CPU profile", "file", f.Name())
			closeProfile(f)
		default:
			cpuprof = f
		}
	}
	if filename := os.Getenv(variable(name, "MEM_PROFILE")); filename != "" {
		f, err := os.Create(path.Clean(filename)) //nolint:gosec // G703: the operator names the profile file in the environment
		if err != nil {
			slog.Error("could not create memory profile", "error", err)
		} else {
			memprof = f
		}
	}
}

// Cleanup stops CPU profiling and writes the memory profile; main defers it.
func Cleanup() {
	if cpuprof != nil {
		pprof.StopCPUProfile()
		closeProfile(cpuprof)
	}
	if memprof != nil {
		runtime.GC() // get up-to-date statistics
		if err := pprof.WriteHeapProfile(memprof); err != nil {
			slog.Error("could not write memory profile", "error", err)
		}
		closeProfile(memprof)
	}
}

// closeProfile closes a profile file, reporting a failure, which can lose
// the end of the profile.
func closeProfile(f *os.File) {
	if err := f.Close(); err != nil {
		slog.Error("could not close profile", "file", f.Name(), "error", err)
	}
}

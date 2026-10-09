package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/dihedron/lxd-spiffe/cmd/lxd-probe/command"
	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/jessevdk/go-flags"
	"github.com/joho/godotenv"
)

func main() {
	os.Exit(run())
}

// run parses the command line and runs the command; it returns the exit
// code so that the deferred cleanup runs before the process exits.
func run() int {
	defer cleanup()

	// every log line goes through the evidence sanitizer (PRN-11)
	slog.SetDefault(slog.New(evidence.NewLogHandler(slog.Default().Handler(), evidence.Logs)))

	err := godotenv.Load()
	if err != nil {
		slog.Warn("error loading .env file", "error", err)
	}

	options := command.Commands{}
	_, err = flags.NewParser(&options, flags.Default).Parse()
	return exitCode(err)
}

// exitCode maps the result of the parser to the exit codes of the spec
// (PRN-34): help is a success, any other parser error is a usage error, and
// command errors carry their own code.
func exitCode(err error) int {
	var flagsErr *flags.Error
	if errors.As(err, &flagsErr) {
		if flagsErr.Type == flags.ErrHelp {
			return exit.Success
		}
		return exit.Usage
	}
	return exit.Code(err)
}

package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/jessevdk/go-flags"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"help", &flags.Error{Type: flags.ErrHelp}, 0},
		{"unknown flag", &flags.Error{Type: flags.ErrUnknownFlag}, exit.Usage},
		{"command required", &flags.Error{Type: flags.ErrCommandRequired}, exit.Usage},
		{"refused", exit.New(exit.Refused, errors.New("missing --lab")), exit.Refused},
		{"wrapped timeout", fmt.Errorf("server info: %w", exit.New(exit.Timeout, errors.New("deadline"))), exit.Timeout},
		{"internal", errors.New("boom"), exit.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d; want %d", tt.err, got, tt.want)
			}
		})
	}
}

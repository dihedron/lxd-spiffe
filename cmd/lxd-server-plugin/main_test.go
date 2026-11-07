package main

import (
	"os"
	"testing"
)

// TestRun runs the command line parser on a command, help and an unknown
// command: help is a success.
func TestRun(t *testing.T) {
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	for _, v := range []struct {
		args []string
		want int
	}{
		{[]string{"lxd-server-plugin", "version"}, 0},
		{[]string{"lxd-server-plugin", "--help"}, 0},
		{[]string{"lxd-server-plugin", "bogus"}, 1},
	} {
		os.Args = v.args
		if got := run(); got != v.want {
			t.Errorf("%v: exit code %d, want %d", v.args, got, v.want)
		}
	}
}

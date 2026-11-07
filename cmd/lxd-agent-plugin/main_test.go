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
		{[]string{"lxd-agent-plugin", "version"}, 0},
		{[]string{"lxd-agent-plugin", "--help"}, 0},
		{[]string{"lxd-agent-plugin", "bogus"}, 1},
	} {
		os.Args = v.args
		if got := run(); got != v.want {
			t.Errorf("%v: exit code %d, want %d", v.args, got, v.want)
		}
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
	cleanup()
	if info, err := os.Stat(dir + "/mem"); err != nil || info.Size() == 0 {
		t.Errorf("memory profile not written: %v", err)
	}
}

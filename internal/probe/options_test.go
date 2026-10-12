package probe

import (
	"os"
	"slices"
	"testing"

	"github.com/jessevdk/go-flags"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func TestUIDListUnmarshalFlag(t *testing.T) {
	tests := []struct {
		value string
		want  UIDList
		ok    bool
	}{
		{"0", UIDList{0}, true},
		{"0,1000,65534", UIDList{0, 1000, 65534}, true},
		{"0, 1000", UIDList{0, 1000}, true},
		{"", nil, false},
		{"0,,1000", nil, false},
		{"-1", nil, false},
		{"root", nil, false},
		{"4294967296", nil, false},
	}
	for _, tt := range tests {
		var got UIDList
		err := got.UnmarshalFlag(tt.value)
		if (err == nil) != tt.ok {
			t.Errorf("UnmarshalFlag(%q) = %v; want ok=%v", tt.value, err, tt.ok)
			continue
		}
		if tt.ok && !slices.Equal(got, tt.want) {
			t.Errorf("UnmarshalFlag(%q) = %v; want %v", tt.value, got, tt.want)
		}
	}
}

func TestNotImplemented(t *testing.T) {
	err := NotImplemented("server info")
	if exit.Code(err) != exit.Internal {
		t.Errorf("exit code = %d; want %d", exit.Code(err), exit.Internal)
	}
	if err.Error() != "server info: not implemented yet" {
		t.Errorf("Error() = %q", err.Error())
	}
}

// TestRecordHostFromEnvironment checks that LXD_PROBE_RECORD_HOST sets
// --record-host, as lab.sh passes it ("unset": not in the environment).
func TestRecordHostFromEnvironment(t *testing.T) {
	for value, want := range map[string]bool{"unset": false, "true": true, "false": false} {
		if value == "unset" {
			t.Setenv("LXD_PROBE_RECORD_HOST", "")
			if err := os.Unsetenv("LXD_PROBE_RECORD_HOST"); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Setenv("LXD_PROBE_RECORD_HOST", value)
		}
		var g GlobalOptions
		if _, err := flags.NewParser(&g, flags.None).ParseArgs(nil); err != nil {
			t.Fatalf("LXD_PROBE_RECORD_HOST=%q: %v", value, err)
		}
		if g.RecordHost != want {
			t.Errorf("LXD_PROBE_RECORD_HOST=%q: RecordHost %v; want %v", value, g.RecordHost, want)
		}
	}
}

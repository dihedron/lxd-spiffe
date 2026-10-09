package probe

import (
	"slices"
	"testing"

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

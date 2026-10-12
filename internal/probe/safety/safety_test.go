package safety

import (
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

func TestWrite(t *testing.T) {
	tests := []struct {
		allowWrite, lab bool
		ok              bool
	}{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{true, true, true},
	}
	for _, tt := range tests {
		err := Write(tt.allowWrite, tt.lab)
		if (err == nil) != tt.ok {
			t.Errorf("Write(%v, %v) = %v; want ok=%v", tt.allowWrite, tt.lab, err, tt.ok)
		}
		if err != nil && exit.Code(err) != exit.Refused {
			t.Errorf("Write(%v, %v) exit code = %d; want %d", tt.allowWrite, tt.lab, exit.Code(err), exit.Refused)
		}
	}
}

func TestKey(t *testing.T) {
	tests := []struct {
		key        string
		allowSpire bool
		ok         bool
	}{
		{"user.lxd-probe.a", false, true},
		{"user.lxd-probe.t1", false, true},
		{"user.lxd-probe.nested.key_1-x", false, true},
		{"user.lxd-probe.", false, false},
		{"user.lxd-probe", false, false},
		{"user.lxd-probex.a", false, false},
		{"user.lxd-probe.a b", false, false},
		{"user.lxd-probe.a\n", false, false},
		{"user.lxd-probe.a/b", false, false},
		{"user.other", false, false},
		{"security.privileged", false, false},
		{"volatile.uuid", false, false},
		{"", false, false},
		{"user.spire.challenge.0123abcd", false, false},
		{"user.spire.challenge.0123abcd", true, true},
		{"user.spire.challenge.", true, false},
		{"user.spire.other", true, false},
		{"user.lxd-probe.a", true, true},
	}
	for _, tt := range tests {
		err := Key(tt.key, tt.allowSpire)
		if (err == nil) != tt.ok {
			t.Errorf("Key(%q, %v) = %v; want ok=%v", tt.key, tt.allowSpire, err, tt.ok)
		}
		if err != nil && exit.Code(err) != exit.Refused {
			t.Errorf("Key(%q, %v) exit code = %d; want %d", tt.key, tt.allowSpire, exit.Code(err), exit.Refused)
		}
	}
}

func TestPath(t *testing.T) {
	tests := []struct {
		path       string
		allowSpire bool
		ok         bool
	}{
		{"/run/lxd-probe/proof", false, true},
		{"/run/lxd-probe/layout/link", false, true},
		{"/run/lxd-probe", false, false},
		{"/run/lxd-probe/", false, false},
		{"/run/lxd-probe/../shadow", false, false},
		{"/run/lxd-probe/./proof", false, false},
		{"/run/lxd-probe//proof", false, false},
		{"/run/lxd-probe/proof/", false, false},
		{"/run/lxd-probex/proof", false, false},
		{"run/lxd-probe/proof", false, false},
		{"/etc/shadow", false, false},
		{"", false, false},
		{"/run/spire/lxd/proof-0123", false, false},
		{"/run/spire/lxd/proof-0123", true, true},
		{"/run/spire/lxd", true, false},
		{"/run/spire/lxdx/proof", true, false},
		{"/run/spire/other", true, false},
		{"/run/lxd-probe/proof", true, true},
	}
	for _, tt := range tests {
		err := Path(tt.path, tt.allowSpire)
		if (err == nil) != tt.ok {
			t.Errorf("Path(%q, %v) = %v; want ok=%v", tt.path, tt.allowSpire, err, tt.ok)
		}
		if err != nil && exit.Code(err) != exit.Refused {
			t.Errorf("Path(%q, %v) exit code = %d; want %d", tt.path, tt.allowSpire, exit.Code(err), exit.Refused)
		}
	}
}

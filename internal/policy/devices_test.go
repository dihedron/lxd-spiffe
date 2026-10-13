package policy

import (
	"slices"
	"testing"
)

func names(fs []DeviceFinding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Device+":"+string(f.Relation))
	}
	slices.Sort(out)
	return out
}

func TestCheckIsolation(t *testing.T) {
	root := map[string]string{"type": "disk", "path": "/", "pool": "default"}
	nic := map[string]string{"type": "nic", "network": "lxdbr0", "name": "eth0"}
	for _, tc := range []struct {
		name    string
		devices map[string]map[string]string
		reject  bool
		causes  []string
		info    []string
	}{
		{name: "root disk and nic only", devices: map[string]map[string]string{"root": root, "eth0": nic}},
		{name: "host directory at the proof directory", reject: true, causes: []string{"shared:equal"},
			devices: map[string]map[string]string{"root": root, "shared": {"type": "disk", "source": "/srv/shared", "path": "/run/lxd-probe"}}},
		{name: "trailing slash and dot segments", reject: true, causes: []string{"shared:equal"},
			devices: map[string]map[string]string{"shared": {"type": "disk", "source": "/srv/shared", "path": "/run/./lxd-probe/"}}},
		{name: "custom volume at an ancestor", reject: true, causes: []string{"vol:ancestor"},
			devices: map[string]map[string]string{"vol": {"type": "disk", "pool": "default", "source": "v1", "path": "/run"}}},
		{name: "below the proof directory", info: []string{"sub:below"},
			devices: map[string]map[string]string{"sub": {"type": "disk", "source": "/srv/x", "path": "/run/lxd-probe/sub"}}},
		{name: "a sibling with a common prefix is unrelated",
			devices: map[string]map[string]string{"near": {"type": "disk", "source": "/srv/x", "path": "/run/lxd-probe2"}}},
		{name: "a block disk without a path", info: []string{"blk:no-path"},
			devices: map[string]map[string]string{"blk": {"type": "disk", "pool": "default", "source": "b1"}}},
		{name: "other device types with a path", info: []string{"tty:other-type"},
			devices: map[string]map[string]string{"tty": {"type": "unix-char", "source": "/dev/ttyS0", "path": "/run/lxd-probe/tty"}}},
		{name: "a relative path fails closed", reject: true, causes: []string{"odd:not-absolute"},
			devices: map[string]map[string]string{"odd": {"type": "disk", "source": "/srv/x", "path": "run/lxd-probe"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckIsolation(tc.devices, "/run/lxd-probe")
			if got.Reject != tc.reject {
				t.Errorf("reject = %v; want %v", got.Reject, tc.reject)
			}
			if c := names(got.Causes); !slices.Equal(c, tc.causes) {
				t.Errorf("causes = %v; want %v", c, tc.causes)
			}
			if i := names(got.Info); !slices.Equal(i, tc.info) {
				t.Errorf("info = %v; want %v", i, tc.info)
			}
		})
	}
}

func TestDiskSource(t *testing.T) {
	for _, tc := range []struct {
		device map[string]string
		want   string
	}{
		{map[string]string{"type": "disk", "source": "/srv/shared", "path": "/x"}, "host:/srv/shared"},
		{map[string]string{"type": "disk", "pool": "default", "source": "v1", "path": "/x"}, "volume:default/v1"},
		{map[string]string{"type": "disk", "path": "/", "pool": "default"}, ""},
		{map[string]string{"type": "nic"}, ""},
	} {
		if got := DiskSource(tc.device); got != tc.want {
			t.Errorf("DiskSource(%v) = %q; want %q", tc.device, got, tc.want)
		}
	}
}

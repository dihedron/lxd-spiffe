package guest

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/evidence"
	"github.com/dihedron/lxd-spiffe/internal/probe"
)

func TestParseIDMap(t *testing.T) {
	got, err := parseIDMap("         0    1000000 1000000000\n      1000       1000          1\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []idmapEntry{{Inside: 0, Outside: 1000000, Count: 1000000000}, {Inside: 1000, Outside: 1000, Count: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseIDMap = %+v; want %+v", got, want)
	}
	for _, bad := range []string{"0 1000000", "a b c", "0 1 2 3"} {
		if _, err := parseIDMap(bad); err == nil {
			t.Errorf("parseIDMap(%q) accepted", bad)
		}
	}
}

func TestParseMountinfo(t *testing.T) {
	const mountinfo = `24 1 0:21 / / rw,relatime shared:1 - zfs default/containers/c1 rw,xattr,posixacl
30 24 0:25 / /run rw,nosuid,nodev,noexec,relatime shared:5 - tmpfs tmpfs rw,size=100m,mode=755,uid=1000000,gid=1000000
31 30 0:26 / /run/lock rw,nosuid,nodev,noexec,relatime shared:6 - tmpfs tmpfs rw,size=5120k
`
	got, ok := findMount(mountinfo, "/run")
	want := mount{MountPoint: "/run", FSType: "tmpfs", Source: "tmpfs", Options: "rw,nosuid,nodev,noexec,relatime", SuperOptions: "rw,size=100m,mode=755,uid=1000000,gid=1000000"}
	if !ok || got != want {
		t.Errorf("findMount(/run) = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := findMount(mountinfo, "/srv"); ok {
		t.Error("found a mount that is not there")
	}
	// the last mount on a point is the visible one
	twice := mountinfo + "40 24 0:30 / /run rw - ramfs ramfs rw\n"
	if got, _ := findMount(twice, "/run"); got.FSType != "ramfs" {
		t.Errorf("overmounted /run: %+v", got)
	}
}

// fakeRoot builds a guest's /proc and /run under a temporary directory.
func fakeRoot(t *testing.T, container bool) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"proc/self/uid_map":         "         0    1000000 1000000000\n",
		"proc/self/gid_map":         "         0    1000000 1000000000\n",
		"proc/self/mountinfo":       "30 24 0:25 / /run rw,nosuid,nodev - tmpfs tmpfs rw,size=100m\n",
		"proc/sys/kernel/osrelease": "6.8.0-45-generic\n",
		"proc/cpuinfo":              "processor\t: 0\nflags\t\t: fpu vme hypervisor\n",
		"proc/1/comm":               "systemd\n",
		"proc/431/comm":             "lxd-agent\n",
		"proc/self/comm":            "lxd-probe\n",
	}
	if container {
		files["run/systemd/container"] = "lxc\n"
		delete(files, "proc/431/comm")
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// socket creates a Unix socket and returns its path.
func socket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	path := filepath.Join(dir, "sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() }) //nolint:errcheck
	return path
}

// detectVirt writes a stand-in for systemd-detect-virt that prints out.
func detectVirt(t *testing.T, out string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "systemd-detect-virt")
	script := "#!/bin/sh\necho " + out + "\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runPreflight(t *testing.T, root, sock, virt string) evidence.Record {
	t.Helper()
	cmd := &Preflight{
		GuestCommand: probe.GuestCommand{GlobalOptions: probe.GlobalOptions{EvidenceDir: t.TempDir(), JSON: true}, Socket: sock},
		root:         root, detectVirt: virt, stdout: &bytes.Buffer{},
	}
	if err := cmd.Execute(nil); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	var rec evidence.Record
	if err := json.Unmarshal(cmd.stdout.(*bytes.Buffer).Bytes(), &rec); err != nil {
		t.Fatalf("printed record: %v", err)
	}
	return rec
}

func value(t *testing.T, rec evidence.Record, key string) any {
	t.Helper()
	for _, o := range rec.Observations {
		if o.Key == key {
			return o.Value
		}
	}
	var keys []string
	for _, o := range rec.Observations {
		keys = append(keys, o.Key)
	}
	t.Fatalf("no observation %q; has %v", key, keys)
	return nil
}

func jsonOf(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestPreflightVM(t *testing.T) {
	sock := socket(t)
	rec := runPreflight(t, fakeRoot(t, false), sock, detectVirt(t, "kvm", 0))
	if rec.Env != "guest" || rec.Verb != "preflight" || rec.Outcome != evidence.OutcomeOK || rec.ID != "guest/0001" {
		t.Errorf("record %s %s %s %s", rec.ID, rec.Env, rec.Verb, rec.Outcome)
	}
	for key, want := range map[string]string{
		"idmap.uid_map":   `[{"count":1000000000,"inside":0,"outside":1000000}]`,
		"kernel.release":  `"6.8.0-45-generic"`,
		"lxd_agent.pids":  `[431]`,
		"run.mount":       `{"fstype":"tmpfs","mount_point":"/run","options":"rw,nosuid,nodev","source":"tmpfs","super_options":"rw,size=100m"}`,
		"virt.guess":      `"vm"`,
		"virt.detect":     `{"available":true,"exit_code":0,"output":"kvm"}`,
		"devlxd.socket":   `{"exists":true,"gid":` + jsonOf(os.Getgid()) + `,"is_socket":true,"mode":"0755","path":` + jsonOf(sock) + `,"uid":` + jsonOf(os.Getuid()) + `}`,
		"ids.uid":         jsonOf(os.Getuid()),
		"ids.euid":        jsonOf(os.Geteuid()),
		"ids.gid":         jsonOf(os.Getgid()),
		"idmap.gid_map":   `[{"count":1000000000,"inside":0,"outside":1000000}]`,
		"lxd_agent.found": `true`,
	} {
		got := jsonOf(value(t, rec, key))
		if key == "devlxd.socket" {
			// the socket's mode depends on the umask: compare without it
			var m map[string]any
			_ = json.Unmarshal([]byte(got), &m)
			m["mode"] = "0755"
			got = jsonOf(m)
		}
		if got != want {
			t.Errorf("%s = %s; want %s", key, got, want)
		}
	}
}

func TestPreflightContainer(t *testing.T) {
	rec := runPreflight(t, fakeRoot(t, true), filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "absent"))
	for key, want := range map[string]string{
		"virt.guess":      `"container"`,
		"virt.detect":     `{"available":false}`,
		"lxd_agent.found": `false`,
		"lxd_agent.pids":  `[]`,
	} {
		if got := jsonOf(value(t, rec, key)); got != want {
			t.Errorf("%s = %s; want %s", key, got, want)
		}
	}
	sock := value(t, rec, "devlxd.socket").(map[string]any)
	if sock["exists"] != false || !strings.Contains(jsonOf(sock), "error") {
		t.Errorf("missing socket: %v", sock)
	}
	// systemd-detect-virt answering "none" with exit 1 is recorded as is
	rec = runPreflight(t, fakeRoot(t, true), filepath.Join(t.TempDir(), "absent"), detectVirt(t, "none", 1))
	if got := jsonOf(value(t, rec, "virt.detect")); got != `{"available":true,"exit_code":1,"output":"none"}` {
		t.Errorf("virt.detect = %s", got)
	}
}

func TestPreflightUnreadable(t *testing.T) {
	root := fakeRoot(t, true)
	if err := os.Remove(filepath.Join(root, "proc/self/mountinfo")); err != nil {
		t.Fatal(err)
	}
	rec := runPreflight(t, root, filepath.Join(t.TempDir(), "absent"), "")
	// what cannot be read is recorded, not a failure (PRF-01)
	if rec.Outcome != evidence.OutcomeOK {
		t.Errorf("outcome %s", rec.Outcome)
	}
	if v := value(t, rec, "run.mount.error"); !strings.Contains(jsonOf(v), "mountinfo") {
		t.Errorf("run.mount.error = %v", v)
	}
}

package guest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// Preflight is guest preflight (PRF-01, PRF-02): the guest's view of itself,
// read-only and without privileges beyond the caller's. What cannot be read
// is recorded as <key>.error, not as a failure.
type Preflight struct {
	probe.GuestCommand

	// root is where /proc and /run are read: "/" but in tests.
	root string
	// detectVirt is the systemd-detect-virt to run: an absolute path in
	// tests, empty otherwise (found in the system directories).
	detectVirt string
	stdout     io.Writer
}

// Execute runs the command.
func (cmd *Preflight) Execute(args []string) error {
	if cmd.root == "" {
		cmd.root = "/"
	}
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "preflight", nil)
	if err != nil {
		return err
	}
	cmd.ids(s)
	cmd.idmaps(s)
	cmd.kernel(s)
	cmd.socket(s)
	cmd.agent(s)
	cmd.run(s)
	cmd.virt(s)
	return s.finish(nil)
}

func (cmd *Preflight) path(name string) string {
	return filepath.Join(cmd.root, name)
}

func (cmd *Preflight) ids(s *session) {
	s.observe("ids.uid", os.Getuid())
	s.observe("ids.euid", os.Geteuid())
	s.observe("ids.gid", os.Getgid())
	s.observe("ids.egid", os.Getegid())
	if groups, err := os.Getgroups(); err != nil {
		s.observe("ids.groups.error", err.Error())
	} else {
		s.observe("ids.groups", groups)
	}
}

func (cmd *Preflight) idmaps(s *session) {
	for _, name := range []string{"uid_map", "gid_map"} {
		key := "idmap." + name
		data, err := os.ReadFile(cmd.path("proc/self/" + name))
		if err != nil {
			s.observe(key+".error", err.Error())
			continue
		}
		entries, err := parseIDMap(string(data))
		if err != nil {
			s.observe(key+".error", err.Error())
			s.observe(key+".raw", string(data))
			continue
		}
		s.observe(key, entries)
	}
}

func (cmd *Preflight) kernel(s *session) {
	data, err := os.ReadFile(cmd.path("proc/sys/kernel/osrelease"))
	if err != nil {
		s.observe("kernel.release.error", err.Error())
		return
	}
	s.observe("kernel.release", strings.TrimSpace(string(data)))
}

// socketInfo is the devlxd.socket observation.
type socketInfo struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	IsSocket bool   `json:"is_socket,omitempty"`
	Mode     string `json:"mode,omitempty"`
	UID      *int   `json:"uid,omitempty"`
	GID      *int   `json:"gid,omitempty"`
	Error    string `json:"error,omitempty"`
}

// socket records the devLXD socket as the caller sees it, without opening
// it: the option is the caller's path, not one under root.
func (cmd *Preflight) socket(s *session) {
	info := socketInfo{Path: cmd.Socket}
	fi, err := os.Stat(cmd.Socket)
	if err != nil {
		info.Error = err.Error()
		s.observe("devlxd.socket", info)
		return
	}
	info.Exists = true
	info.IsSocket = fi.Mode()&os.ModeSocket != 0
	info.Mode = fmt.Sprintf("%04o", fi.Mode().Perm())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		uid, gid := int(st.Uid), int(st.Gid)
		info.UID, info.GID = &uid, &gid
	}
	s.observe("devlxd.socket", info)
}

// agent records the processes named lxd-agent (VMs).
func (cmd *Preflight) agent(s *session) {
	entries, err := os.ReadDir(cmd.path("proc"))
	if err != nil {
		s.observe("lxd_agent.error", err.Error())
		return
	}
	pids := []int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(cmd.path(filepath.Join("proc", e.Name(), "comm")))
		if err == nil && strings.TrimSpace(string(comm)) == "lxd-agent" {
			pids = append(pids, pid)
		}
	}
	slices.Sort(pids)
	s.observe("lxd_agent.found", len(pids) > 0)
	s.observe("lxd_agent.pids", pids)
}

// mount is the run.mount observation: one line of /proc/self/mountinfo.
type mount struct {
	MountPoint   string `json:"mount_point"`
	FSType       string `json:"fstype"`
	Source       string `json:"source"`
	Options      string `json:"options"`
	SuperOptions string `json:"super_options"`
}

func (cmd *Preflight) run(s *session) {
	data, err := os.ReadFile(cmd.path("proc/self/mountinfo"))
	if err != nil {
		s.observe("run.mount.error", err.Error())
		return
	}
	if m, ok := findMount(string(data), "/run"); ok {
		s.observe("run.mount", m)
	} else {
		s.observe("run.mount", nil)
	}
}

// detectVirtDirs are where systemd-detect-virt is looked for. PATH is not
// searched: preflight runs as root, and a writable directory early in an
// inherited PATH could hold another program of that name.
var detectVirtDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// findDetectVirt returns the systemd-detect-virt to run: override when
// set (tests), else the first of detectVirtDirs, if it is an executable
// regular file; "" when there is none.
func findDetectVirt(override string) string {
	candidates := []string{override}
	if override == "" {
		candidates = candidates[:0]
		for _, dir := range detectVirtDirs {
			candidates = append(candidates, filepath.Join(dir, "systemd-detect-virt"))
		}
	}
	for _, path := range candidates {
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

// virt records systemd-detect-virt's answer when it is there, and a guess
// from /run and /proc. A container is told first: one on a VM sees the
// hypervisor flag of its host's CPU too.
func (cmd *Preflight) virt(s *session) {
	detect := map[string]any{"available": false}
	if path := findDetectVirt(cmd.detectVirt); path != "" {
		timeout := cmd.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var out bytes.Buffer
		c := exec.CommandContext(ctx, path) //nolint:gosec // G204: systemd-detect-virt from a system directory, no arguments, no shell
		c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
		c.Dir = "/"
		c.Stdout = &out
		err := c.Run()
		code := 0
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee):
			code = ee.ExitCode()
		case err != nil:
			code = -1
			detect["error"] = err.Error()
		}
		detect["available"] = true
		detect["output"] = strings.TrimSpace(out.String())
		detect["exit_code"] = code
	}
	s.observe("virt.detect", detect)

	guess, why := "unknown", ""
	if data, err := os.ReadFile(cmd.path("run/systemd/container")); err == nil {
		guess, why = "container", "/run/systemd/container: "+strings.TrimSpace(string(data))
	} else if data, err := os.ReadFile(cmd.path("proc/cpuinfo")); err == nil && hasCPUFlag(string(data), "hypervisor") {
		guess, why = "vm", "/proc/cpuinfo: hypervisor flag"
	}
	s.observe("virt.guess", guess)
	if why != "" {
		s.observe("virt.guess_from", why)
	}
}

// idmapEntry is one line of a uid_map or gid_map.
type idmapEntry struct {
	Inside  uint64 `json:"inside"`
	Outside uint64 `json:"outside"`
	Count   uint64 `json:"count"`
}

// parseIDMap parses /proc/<pid>/uid_map or gid_map: lines of three numbers.
func parseIDMap(text string) ([]idmapEntry, error) {
	entries := []idmapEntry{}
	for line := range strings.Lines(text) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("idmap line %q: want 3 fields", strings.TrimSpace(line))
		}
		var n [3]uint64
		for i := range f {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("idmap line %q: %w", strings.TrimSpace(line), err)
			}
			n[i] = v
		}
		entries = append(entries, idmapEntry{Inside: n[0], Outside: n[1], Count: n[2]})
	}
	return entries, nil
}

// findMount returns the visible (last) mount on point in a mountinfo text:
// "id parent major:minor root point options [optional...] - fstype source
// super-options".
func findMount(mountinfo, point string) (mount, bool) {
	var found mount
	ok := false
	sc := bufio.NewScanner(strings.NewReader(mountinfo))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := slices.Index(f, "-")
		if len(f) < 6 || sep < 6 || len(f) < sep+4 || f[4] != point {
			continue
		}
		found = mount{MountPoint: f[4], Options: f[5], FSType: f[sep+1], Source: f[sep+2], SuperOptions: f[sep+3]}
		ok = true
	}
	return found, ok
}

// hasCPUFlag reports whether a "flags" line of /proc/cpuinfo has flag.
func hasCPUFlag(cpuinfo, flag string) bool {
	for line := range strings.Lines(cpuinfo) {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == "flags" && slices.Contains(strings.Fields(value), flag) {
			return true
		}
	}
	return false
}

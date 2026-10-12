package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
)

// DevlxdAccess is guest devlxd access --as-uid N[,N...] [--path P]...
// (PRF-13): who in the guest can use devLXD (gate L4).
type DevlxdAccess struct {
	probe.GuestCommand
	AsUID probe.UIDList `long:"as-uid" description:"Comma-separated uids to try." required:"yes"`
	Path  []string      `long:"path" description:"devLXD path to try as each uid; repeatable." default:"/1.0"`

	stdout io.Writer
}

// DevlxdAccessChild is the hidden child of guest devlxd access: the calls
// as the current uid, their statuses on stdout as JSON, never a body.
type DevlxdAccessChild struct {
	probe.GuestCommand
	Path []string `long:"path" description:"devLXD path to try; repeatable." default:"/1.0"`

	stdout io.Writer
}

// childReport is what the child prints.
type childReport struct {
	UID     int           `json:"uid"`
	GID     int           `json:"gid"`
	Results []childResult `json:"results"`
}

// childResult is one call of the child: its status and format, or its
// transport error (status 0).
type childResult struct {
	Path   string `json:"path"`
	Status int    `json:"status,omitempty"`
	Format string `json:"format,omitempty"`
	Error  string `json:"error,omitempty"`
}

// accessResult is a childResult as recorded, with its verdict.
type accessResult struct {
	childResult
	Allowed bool `json:"allowed"`
}

// isRoot and startChild are replaced in tests: switching uid needs root.
var (
	isRoot     = func() bool { return os.Geteuid() == 0 }
	startChild = execChild
)

// Execute runs the command.
func (cmd *DevlxdAccess) Execute(args []string) error {
	if !isRoot() {
		return exit.New(exit.Usage, errors.New("guest devlxd access: needs root, to run its calls as other uids"))
	}
	uids := make([]string, len(cmd.AsUID))
	for i, uid := range cmd.AsUID {
		uids[i] = strconv.FormatUint(uint64(uid), 10)
	}
	recorded := []string{"--as-uid", strings.Join(uids, ",")}
	for _, p := range cmd.Path {
		recorded = append(recorded, "--path", p)
	}
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "devlxd access", recorded)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cmd.Timeout))
	defer cancel()
	childArgs := []string{"guest", "devlxd", "access-child", "--socket", cmd.Socket}
	for _, p := range cmd.Path {
		childArgs = append(childArgs, "--path", p)
	}
	for _, uid := range cmd.AsUID {
		gid := primaryGID(uid)
		key := fmt.Sprintf("access.uid.%d", uid)
		out, err := startChild(ctx, uid, gid, childArgs)
		var rep childReport
		if err == nil {
			err = json.Unmarshal(out, &rep)
		}
		if err != nil {
			// recorded, not a failure of the command (PRF-13)
			s.observe(key, map[string]any{"gid": gid, "error": err.Error()})
			continue
		}
		results := make([]accessResult, len(rep.Results))
		for i, r := range rep.Results {
			results[i] = accessResult{childResult: r, Allowed: r.Status >= 200 && r.Status <= 299}
		}
		s.observe(key, map[string]any{"gid": rep.GID, "results": results})
	}
	return s.finish(nil)
}

// primaryGID returns uid's primary gid from the passwd database, or the
// same number when uid has no entry (PRF-13).
func primaryGID(uid uint32) uint32 {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return uid
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return uid
	}
	return uint32(gid)
}

// execChild runs this binary again as uid and gid, without supplementary
// groups, and returns its stdout.
func execChild(ctx context.Context, uid, gid uint32, args []string) ([]byte, error) {
	c := exec.CommandContext(ctx, "/proc/self/exe", args...)
	c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}}
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	c.Dir = "/"
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Execute runs the child: each path as the current uid, statuses only.
func (cmd *DevlxdAccessChild) Execute(args []string) error {
	w := cmd.stdout
	if w == nil {
		w = os.Stdout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout(cmd.Timeout))
	defer cancel()
	rep := childReport{UID: os.Getuid(), GID: os.Getgid(), Results: []childResult{}}
	c, cerr := devlxd.Connect(ctx, devlxd.Config{Socket: cmd.Socket})
	for _, p := range cmd.Path {
		r := childResult{Path: p}
		if cerr != nil {
			r.Error = cerr.Error()
		} else if resp, err := c.Get(ctx, p); err != nil {
			r.Error = err.Error()
		} else {
			r.Status, r.Format = resp.Status, string(resp.Format)
			if resp.Status < 200 || resp.Status > 299 {
				// an error message, never a value
				var v any
				var e *lxd.Error
				if errors.As(resp.Decode(&v), &e) && e.Err != nil {
					r.Error = e.Err.Error()
				}
			}
		}
		rep.Results = append(rep.Results, r)
	}
	return json.NewEncoder(w).Encode(rep)
}

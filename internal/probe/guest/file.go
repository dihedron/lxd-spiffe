package guest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/dihedron/lxd-spiffe/internal/probe"
	"github.com/dihedron/lxd-spiffe/internal/probe/exit"
	"github.com/dihedron/lxd-spiffe/internal/probe/safety"
)

// hostRoot is prepended to the guest directories: "" but in tests.
var hostRoot = ""

// maxContent bounds what file put reads from --content-file.
const maxContent = 1 << 20

// markerName is the marker put writes next to a file under the SPIRE
// directory, so that rm removes only what the probe created (PRF-22).
func markerName(name string) string {
	return "." + name + ".lxd-probe"
}

// labRoot checks a guest write path (PRN-02) and opens an os.Root on the
// directory it belongs to, created mode 0700 when missing (PRN-05). It
// returns the root, the path relative to it, and whether it is the SPIRE
// directory.
func labRoot(p string, allowSpire bool) (*os.Root, string, bool, error) {
	if err := safety.Path(p, allowSpire); err != nil {
		return nil, "", false, err
	}
	base, spire := safety.LabDir, false
	if allowSpire && strings.HasPrefix(p, safety.SpireDir+"/") {
		base, spire = safety.SpireDir, true
	}
	dir := hostRoot + base
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", false, fmt.Errorf("creating %s: %w", base, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", false, err
	}
	return root, strings.TrimPrefix(p, base+"/"), spire, nil
}

// fileInfo is a stat as recorded.
type fileInfo struct {
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Size   int64  `json:"size"`
	Mtime  string `json:"mtime"`
	Inode  uint64 `json:"inode"`
	Nlink  uint64 `json:"nlink"`
	Target string `json:"target,omitempty"`
}

// describe turns a FileInfo into a fileInfo; target is a symlink's.
func describe(fi os.FileInfo, target string) fileInfo {
	info := fileInfo{Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), Size: fi.Size(), Mtime: fi.ModTime().UTC().Format(time.RFC3339Nano), Target: target}
	switch m := fi.Mode(); {
	case m.IsRegular():
		info.Type = "file"
	case m.IsDir():
		info.Type = "dir"
	case m&os.ModeSymlink != 0:
		info.Type = "symlink"
	default:
		info.Type = "other"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		info.UID, info.GID, info.Inode, info.Nlink = int(st.Uid), int(st.Gid), st.Ino, uint64(st.Nlink)
	}
	return info
}

// observeStat records lstat of rel in root as key.
func observeStat(s *session, root *os.Root, rel, key string) {
	fi, err := root.Lstat(rel)
	if err != nil {
		s.observe(key+".error", err.Error())
		return
	}
	target := ""
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ = root.Readlink(rel)
	}
	s.observe(key, describe(fi, target))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// dryRun prints what a write would do (PRN-04).
func dryRun(w io.Writer, format string, args ...any) error {
	if w == nil {
		w = os.Stdout
	}
	_, err := fmt.Fprintf(w, "dry run: "+format+"\n", args...)
	return err
}

// FilePut is guest file put PATH (PRF-20).
type FilePut struct {
	probe.GuestCommand
	ContentFile string `long:"content-file" description:"File holding the content to write; - reads stdin." required:"yes"`
	Mode        uint32 `long:"mode" description:"File mode, in octal." base:"8" default:"0600"`
	Args        struct {
		Path string `positional-arg-name:"PATH" description:"File to create, under /run/lxd-probe/."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *FilePut) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	if err := safety.Path(cmd.Args.Path, cmd.AllowSpirePaths); err != nil {
		return err
	}
	if cmd.Mode&^0o777 != 0 {
		return exit.New(exit.Usage, fmt.Errorf("guest file put: mode %o: permission bits only", cmd.Mode))
	}
	data, err := readContent(cmd.ContentFile)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest file put: %w", err))
	}
	mode := os.FileMode(cmd.Mode)
	if cmd.DryRun {
		return dryRun(cmd.stdout, "would create %s (%d bytes, mode %04o)", cmd.Args.Path, len(data), cmd.Mode)
	}
	root, rel, spire, err := labRoot(cmd.Args.Path, cmd.AllowSpirePaths)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read-only handle on the directory
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "file put", []string{cmd.Args.Path, "--mode", fmt.Sprintf("%04o", cmd.Mode)})
	if err != nil {
		return err
	}
	err = create(root, rel, data, mode)
	if err == nil && spire {
		err = create(root, path.Join(path.Dir(rel), markerName(path.Base(rel))), nil, 0o600)
	}
	if err == nil {
		observeStat(s, root, rel, "file.stat")
		s.observe("file.sha256", sha256Hex(data))
	}
	return s.finish(err)
}

// readContent reads --content-file, or stdin for "-", up to maxContent.
func readContent(name string) ([]byte, error) {
	var r io.Reader = os.Stdin
	if name != "-" {
		f, err := os.Open(name) //nolint:gosec // G304: the operator's --content-file
		if err != nil {
			return nil, err
		}
		defer f.Close() //nolint:errcheck // read only
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxContent+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxContent {
		return nil, fmt.Errorf("content over %d bytes", maxContent)
	}
	return data, nil
}

// create writes a new file in root with O_EXCL and sets its mode exactly,
// whatever the umask; its parents are created mode 0700.
func create(root *os.Root, rel string, data []byte, mode os.FileMode) error {
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close() //nolint:errcheck // the write error is the one to report
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Chmod(rel, mode)
}

// FileStat is guest file stat PATH (PRF-21): lstat and stat of any path,
// read-only.
type FileStat struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"Path to inspect."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *FileStat) Execute(args []string) error {
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "file stat", []string{cmd.Args.Path})
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(cmd.Args.Path); err != nil {
		s.observe("file.lstat.error", err.Error())
	} else {
		target := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ = os.Readlink(cmd.Args.Path)
		}
		s.observe("file.lstat", describe(fi, target))
	}
	if fi, err := os.Stat(cmd.Args.Path); err != nil {
		s.observe("file.stat.error", err.Error())
	} else {
		s.observe("file.stat", describe(fi, ""))
	}
	return s.finish(nil)
}

// FileRm is guest file rm PATH (PRF-22).
type FileRm struct {
	probe.GuestCommand
	Args struct {
		Path string `positional-arg-name:"PATH" description:"File to remove, created by lxd-probe."`
	} `positional-args:"yes" required:"yes"`

	stdout io.Writer
}

// Execute runs the command.
func (cmd *FileRm) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	root, rel, spire, err := labRoot(cmd.Args.Path, cmd.AllowSpirePaths)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read-only handle on the directory
	fi, err := root.Lstat(rel)
	if err != nil {
		return exit.New(exit.Usage, fmt.Errorf("guest file rm: %w", err))
	}
	if fi.IsDir() {
		return exit.New(exit.Refused, fmt.Errorf("guest file rm: %s is a directory", cmd.Args.Path))
	}
	marker := path.Join(path.Dir(rel), markerName(path.Base(rel)))
	if spire {
		if _, err := root.Lstat(marker); err != nil {
			return exit.New(exit.Refused, fmt.Errorf("guest file rm: %s was not created by lxd-probe (no marker)", cmd.Args.Path))
		}
	}
	if cmd.DryRun {
		return dryRun(cmd.stdout, "would remove %s", cmd.Args.Path)
	}
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "file rm", []string{cmd.Args.Path})
	if err != nil {
		return err
	}
	s.observe("file.removed", describe(fi, ""))
	err = root.Remove(rel)
	if err == nil && spire {
		err = root.Remove(marker)
	}
	return s.finish(err)
}

// FileLayout is guest file layout (PRF-23): the hostile layouts of gate L1
// under /run/lxd-probe/layout/, replacing an earlier one.
type FileLayout struct {
	probe.GuestCommand

	stdout io.Writer
}

// layoutTarget is where the layout's symlink points: outside the lab
// directory, as a redirected proof path would (PRF-23).
const layoutTarget = "/etc/hostname"

// layoutOwner owns the layout's other-owner file when the probe runs as
// root.
const layoutOwner = 1000

// Execute runs the command.
func (cmd *FileLayout) Execute(args []string) error {
	if err := safety.Write(cmd.AllowWrite, cmd.Lab); err != nil {
		return err
	}
	const dir = safety.LabDir + "/layout"
	if cmd.DryRun {
		return dryRun(cmd.stdout, "would replace %s with: regular, symlink -> %s, directory, world-writable, other-owner", dir, layoutTarget)
	}
	root, _, _, err := labRoot(dir+"/regular", false)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // read-only handle on the directory
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "file layout", nil)
	if err != nil {
		return err
	}
	return s.finish(cmd.build(s, root))
}

func (cmd *FileLayout) build(s *session, root *os.Root) error {
	if err := root.RemoveAll("layout"); err != nil {
		return err
	}
	if err := root.Mkdir("layout", 0o700); err != nil {
		return err
	}
	if err := create(root, "layout/regular", []byte("regular\n"), 0o600); err != nil {
		return err
	}
	if err := root.Symlink(layoutTarget, "layout/symlink"); err != nil {
		return err
	}
	if err := root.Mkdir("layout/directory", 0o700); err != nil {
		return err
	}
	if err := create(root, "layout/world-writable", []byte("world-writable\n"), 0o666); err != nil {
		return err
	}
	for _, name := range []string{"regular", "symlink", "directory", "world-writable"} {
		observeStat(s, root, "layout/"+name, "layout."+name)
	}
	if os.Geteuid() != 0 {
		s.observe("layout.other-owner", map[string]string{"skipped": "needs root to give a file to another uid"})
		return nil
	}
	if err := create(root, "layout/other-owner", []byte("other-owner\n"), 0o600); err != nil {
		return err
	}
	if err := root.Lchown("layout/other-owner", layoutOwner, layoutOwner); err != nil {
		return errors.Join(fmt.Errorf("giving layout/other-owner to uid %d", layoutOwner), err)
	}
	observeStat(s, root, "layout/other-owner", "layout.other-owner")
	return nil
}

// IdmapShow is guest idmap show (PRF-30).
type IdmapShow struct {
	probe.GuestCommand

	stdout io.Writer
}

// Execute runs the command.
func (cmd *IdmapShow) Execute(args []string) error {
	s, err := newSession(&cmd.GuestCommand, cmd.stdout, "idmap show", nil)
	if err != nil {
		return err
	}
	(&Preflight{root: "/"}).idmaps(s)
	return s.finish(nil)
}

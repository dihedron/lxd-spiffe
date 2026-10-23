// Package agentproof is the guest side of the proof modes (attestor spec
// Trust model → Proof of co-location): the agent plugin's file_pull proof
// file (FR-A7).
package agentproof

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// dirMode and fileMode are the only modes accepted (FR-A7).
const (
	dirMode  = os.ModeDir | 0o700
	fileMode = 0o600
)

// WriteFile writes content as the file name in dir, a directory of owner
// with mode 0700, which is created, with its missing parents, when absent.
// The file is new (O_EXCL, no symlink followed), mode 0600 and synced to
// disk. The returned remove deletes it, best effort, and may be called more
// than once. The directory is checked by path, then again on the handle
// the file is created through, so a directory swapped in between is
// refused.
func WriteFile(dir, name string, content []byte, owner int) (remove func(), err error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("proof file name %q is not a plain name", name)
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		fi, err = create(dir)
	}
	if err != nil {
		return nil, err
	}
	if err := checkDir(dir, fi, owner); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err == nil && !os.SameFile(fi, opened) {
		err = fmt.Errorf("proof directory %s changed while it was opened", dir)
	}
	if err == nil {
		err = checkDir(dir, opened, owner)
	}
	if err == nil {
		err = write(root, name, content)
	}
	if err != nil {
		root.Close() //nolint:errcheck // the first error is the one to report
		return nil, err
	}
	done := false
	return func() {
		if done {
			return
		}
		done = true
		root.Remove(name) //nolint:errcheck // best effort (FR-A7)
		root.Close()      //nolint:errcheck // read-only handle on the directory
	}, nil
}

// create makes dir 0700, whatever the umask, and its missing parents 0700
// less the umask. A dir created meanwhile by another process is returned
// as it is, for the caller to check.
func create(dir string) (os.FileInfo, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
		return os.Lstat(dir)
	} else if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory, 0700 is FR-A7's mode
		return nil, err
	}
	return os.Lstat(dir)
}

// checkDir requires a real directory (not a symlink) of owner with mode
// 0700 and no special bits.
func checkDir(dir string, fi os.FileInfo, owner int) error {
	if mode := fi.Mode() & (os.ModeType | os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); mode != dirMode {
		return fmt.Errorf("proof directory %s has mode %v, want %v", dir, mode, dirMode)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("proof directory %s: no owner information", dir)
	}
	if int64(st.Uid) != int64(owner) {
		return fmt.Errorf("proof directory %s is owned by uid %d, want %d", dir, st.Uid, owner)
	}
	return nil
}

// write creates name in root with the exact mode, writes content and syncs.
func write(root *os.Root, name string, content []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, fileMode)
	if err != nil {
		return err
	}
	err = f.Chmod(fileMode)
	if err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		root.Remove(name) //nolint:errcheck // a partial proof must not stay
	}
	return err
}

package evidence

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// maxEvidenceFile bounds a record or alias map that is read back; the
// largest so far is about 40 KiB.
const maxEvidenceFile = 16 << 20

// readEvidenceFile reads a record or alias map of a run directory. The
// file must be a regular file, not a symlink: a run directory may come
// from elsewhere (a copied lab run, a committed docs/validation run), and
// a symlink in it must not lead the probe into reading another file.
func readEvidenceFile(path string) ([]byte, error) {
	//nolint:gosec // G304: a file of a run directory; symlinks are refused by O_NOFOLLOW
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read only
	if err := checkRegular(f); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvidenceFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEvidenceFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxEvidenceFile)
	}
	return data, nil
}

// checkRegular refuses an open file that is not a regular file (a FIFO or
// a device).
func checkRegular(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", f.Name())
	}
	return nil
}

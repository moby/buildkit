// Package openfile opens files that a build may have placed in a snapshot.
//
// A snapshot is attacker-controlled: an ExecOp can mknod a device node or
// mkfifo with the default capability set. Opening such an inode from the daemon
// resolves it against the host, so the daemon would act on a device the sandbox
// itself is denied, or block indefinitely in open(2) on a fifo with no writer.
// It refuses anything that is not a regular file.
package openfile

import (
	"os"

	"github.com/pkg/errors"
)

// ErrNotRegular is reported when the path exists but is not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// Regular opens p for reading and fails unless p is a regular file. Callers are
// expected to have resolved p within the snapshot already, for example with
// containerd/continuity fs.RootPath.
func Regular(p string) (*os.File, error) {
	return openRegular(p)
}

func checkRegular(f *os.File, name string) error {
	fi, err := f.Stat()
	if err != nil {
		return errors.WithStack(err)
	}
	if !fi.Mode().IsRegular() {
		return errors.WithStack(&os.PathError{Op: "open", Path: name, Err: ErrNotRegular})
	}
	return nil
}

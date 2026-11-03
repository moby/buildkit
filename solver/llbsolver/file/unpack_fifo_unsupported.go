//go:build !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package file

import (
	"os"
	"syscall"
)

const supportsRootFIFO = false

func createRootFIFO(_ *os.Root, name string, _ os.FileMode) error {
	return &os.PathError{Op: "mknodat", Path: name, Err: syscall.ENOSYS}
}

//go:build linux || freebsd || netbsd || openbsd || dragonfly

package file

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const supportsRootFIFO = true

func createRootFIFO(root *os.Root, name string, mode os.FileMode) error {
	parent, err := root.OpenFile(filepath.Dir(name), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer parent.Close()

	if err := unix.Mknodat(int(parent.Fd()), filepath.Base(name), unix.S_IFIFO|uint32(mode.Perm()), 0); err != nil {
		return &os.PathError{Op: "mknodat", Path: name, Err: err}
	}
	return nil
}

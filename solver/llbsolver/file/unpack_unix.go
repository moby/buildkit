//go:build !windows

package file

import (
	"archive/tar"
	"os"
)

func unpackNoSameOwner() bool {
	return false
}

func applyRootOwner(root *os.Root, name string, hdr *tar.Header, noSameOwner bool) error {
	if noSameOwner {
		return nil
	}
	return root.Lchown(name, hdr.Uid, hdr.Gid)
}

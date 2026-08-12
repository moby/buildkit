//go:build windows

package file

import (
	"archive/tar"
	"os"
)

func unpackNoSameOwner() bool {
	return true
}

func applyRootOwner(*os.Root, string, *tar.Header, bool) error {
	return nil
}

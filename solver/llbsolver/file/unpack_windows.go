//go:build windows

package file

import (
	"archive/tar"
	"errors"
	"os"
	"strings"
	"time"
)

func unpackNoSameOwner() bool {
	return true
}

func validateRootTarPath(name string) error {
	// Tar paths use POSIX separators. On Windows, ":" is not valid in a
	// filename and "\" would be interpreted as a path separator, so either
	// character would change the path described by the archive.
	if strings.ContainsAny(name, `:\`) {
		return errors.New("path contains a character Windows cannot represent")
	}
	return nil
}

func applyRootOwner(*os.Root, string, *tar.Header, bool) error {
	return nil
}

func applyRootMode(root *os.Root, name string, mode os.FileMode) error {
	return root.Chmod(name, mode)
}

func applyRootSymlinkTimes(*os.Root, string, time.Time, time.Time) error {
	return nil
}

//go:build !linux && !darwin && !freebsd && !netbsd

package file

import "os"

func setRootSymlinkXattr(*os.Root, string, string, []byte) error {
	return nil
}

func setRootXattr(*os.Root, *os.File, string, string, []byte) error {
	return nil
}

//go:build !linux && !darwin && !freebsd && !netbsd

package file

import "os"

func setRootXattr(*os.Root, *os.File, string, string, []byte) error {
	return nil
}

// +build !windows

package tar

import (
	"os"
)

func isNullDevice(path string) bool {
	return path == os.DevNull
}

func sanitizePath(path string) (string, error) {
	return path, nil
}

func platformLink(inLink Link) error {
	return nil
}

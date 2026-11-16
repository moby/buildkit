package mounts

import (
	"os"

	"github.com/pkg/errors"
)

func hostCacheMountsSupported() error {
	return errors.New("host cache mounts are not supported on Windows")
}

func tryLockFile(*os.File, bool) (bool, error) {
	return false, hostCacheMountsSupported()
}

func unlockFile(*os.File) error {
	return hostCacheMountsSupported()
}

func fileOwnerAndMode(os.FileInfo) (int, int, uint32, error) {
	return 0, 0, 0, hostCacheMountsSupported()
}

func applyOwnerAndMode(string, int, int, uint32) error {
	return hostCacheMountsSupported()
}

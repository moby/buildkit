//go:build linux || darwin
// +build linux darwin

package rcmgr

import (
	"golang.org/x/sys/unix"
)

func getNumFDs() int {
	var l unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &l); err != nil {
		log.Errorw("failed to get fd limit", "error", err)
		return 0
	}
	return int(l.Cur)
}

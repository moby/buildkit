//go:build !linux && !darwin && !windows

package rcmgr

import "runtime"

// TODO: figure out how to get the number of file descriptors on Windows and other systems
func getNumFDs() int {
	log.Warnf("cannot determine number of file descriptors on %s", runtime.GOOS)
	return 0
}

//go:build !windows

package dockerd

import "path/filepath"

const socketScheme = "unix://"

func getDockerdSockPath(sockRoot, id string) string {
	return filepath.Join(sockRoot, id+".sock")
}

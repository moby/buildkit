//go:build !windows && !freebsd && !netbsd && !openbsd

package disk

import (
	"fmt"
	"syscall"
)

func GetDiskStat(root string) (DiskStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return DiskStat{}, fmt.Errorf("could not stat fs at %s: %w", root, err)
	}

	return DiskStat{
		Total:     int64(st.Bsize) * int64(st.Blocks),
		Free:      int64(st.Bsize) * int64(st.Bfree),
		Available: int64(st.Bsize) * int64(st.Bavail),
	}, nil
}

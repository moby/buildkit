//go:build openbsd

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
		Total:     int64(st.F_bsize) * int64(st.F_blocks),
		Free:      int64(st.F_bsize) * int64(st.F_bfree),
		Available: int64(st.F_bsize) * int64(st.F_bavail),
	}, nil
}

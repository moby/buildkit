//go:build netbsd

package disk

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func GetDiskStat(root string) (DiskStat, error) {
	var st unix.Statvfs_t
	if err := unix.Statvfs(root, &st); err != nil {
		return DiskStat{}, fmt.Errorf("could not stat fs at %s: %w", root, err)
	}

	return DiskStat{
		Total:     int64(st.Frsize) * int64(st.Blocks),
		Free:      int64(st.Frsize) * int64(st.Bfree),
		Available: int64(st.Frsize) * int64(st.Bavail),
	}, nil
}

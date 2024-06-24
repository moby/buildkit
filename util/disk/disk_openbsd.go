//go:build openbsd
// +build openbsd

package disk

import (
	"syscall"
)

func GetDiskSize(root string) (free int64, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return 0, 0, err
	}
	diskSize := int64(st.F_bsize) * int64(st.F_blocks)
	freeSize := int64(st.F_bsize) * int64(st.F_bfree)
	return diskSize, freeSize, nil
}

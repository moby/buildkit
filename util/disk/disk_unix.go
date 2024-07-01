//go:build !windows && !openbsd
// +build !windows,!openbsd

package disk

import (
	"syscall"
)

func GetDiskSize(root string) (free int64, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return 0, 0, err
	}
	diskSize := int64(st.Bsize) * int64(st.Blocks)
	freeSize := int64(st.Bsize) * int64(st.Bfree)
	return diskSize, freeSize, nil
}

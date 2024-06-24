//go:build windows
// +build windows

package disk

import (
	"golang.org/x/sys/windows"
)

func GetDiskSize(root string) (free int64, total int64, err error) {
	rootUTF16, err := windows.UTF16FromString(root)
	if err != nil {
		return 0, 0, err
	}
	var freeAvailableBytes uint64
	var totalBytes uint64
	var totalFreeBytes uint64

	if err := windows.GetDiskFreeSpaceEx(
		&rootUTF16[0],
		&freeAvailableBytes,
		&totalBytes,
		&totalFreeBytes); err != nil {
		return 0, 0, err
	}
	return int64(totalBytes), int64(totalFreeBytes), nil
}

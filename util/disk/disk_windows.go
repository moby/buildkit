//go:build windows

package disk

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func GetDiskStat(root string) (DiskStat, error) {
	rootUTF16, err := windows.UTF16FromString(root)
	if err != nil {
		return DiskStat{}, fmt.Errorf("could not encode %s: %w", root, err)
	}
	var (
		totalBytes         uint64
		totalFreeBytes     uint64
		freeAvailableBytes uint64
	)
	if err := windows.GetDiskFreeSpaceEx(
		&rootUTF16[0],
		&freeAvailableBytes,
		&totalBytes,
		&totalFreeBytes); err != nil {
		return DiskStat{}, fmt.Errorf("could not stat fs at %s: %w", root, err)
	}

	return DiskStat{
		Total:     int64(totalBytes),
		Free:      int64(totalFreeBytes),
		Available: int64(freeAvailableBytes),
	}, nil
}

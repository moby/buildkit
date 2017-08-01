// +build !windows

package contenthash

import (
	"os"
	"syscall"

	"github.com/tonistiigi/fsutil"
)

func chmodWindowsTarEntry(perm os.FileMode) os.FileMode {
	return perm
}

func setUnixOpt(fi os.FileInfo, stat *fsutil.Stat) {
	s := fi.Sys().(*syscall.Stat_t)

	stat.Uid = s.Uid
	stat.Gid = s.Gid

	if !fi.IsDir() {
		if s.Mode&syscall.S_IFBLK != 0 ||
			s.Mode&syscall.S_IFCHR != 0 {
			stat.Devmajor = int64(major(uint64(s.Rdev)))
			stat.Devminor = int64(minor(uint64(s.Rdev)))
		}
	}
}

func major(device uint64) uint64 {
	return (device >> 8) & 0xfff
}

func minor(device uint64) uint64 {
	return (device & 0xff) | ((device >> 12) & 0xfff00)
}

//go:build !windows

package main

import (
	"syscall"

	copy "github.com/tonistiigi/fsutil/copy"
)

func init() {
	syscall.Umask(0)
	copy.UmaskIsZero = true
}

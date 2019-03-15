// +build !windows

package main

import (
	"syscall"
)

func init() {
	syscall.Umask(0)
}

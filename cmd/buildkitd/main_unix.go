// +build !windows

package main

import (
	"syscall"

	"github.com/moby/buildkit/util/network/netns_create"
)

func init() {
	netns_create.Handle()

	syscall.Umask(0)
}

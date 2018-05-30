// +build linux

package main

import "github.com/opencontainers/runc/libcontainer/system"

func runningAsUnprivilegedUser() bool {
	return system.GetParentNSeuid() != 0 || system.RunningInUserNS()
}

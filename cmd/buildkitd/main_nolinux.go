//go:build !linux

package main

import (
	"os"

	"github.com/moby/sys/reexec"
)

func init() {
	if reexec.Init() {
		os.Exit(0)
	}
}

//go:build gccgo
// +build gccgo

package gonso

import _ "unsafe"

//go:linkname beforeFork syscall.runtime__BeforeFork
func beforeFork()

//go:linkname afterFork syscall.runtime__AfterFork
func afterFork()

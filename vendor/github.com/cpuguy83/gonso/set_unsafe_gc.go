//go:build gc
// +build gc

package gonso

import _ "unsafe"

//go:linkname beforeFork syscall.runtime_BeforeFork
func beforeFork()

//go:linkname afterFork syscall.runtime_AfterFork
func afterFork()

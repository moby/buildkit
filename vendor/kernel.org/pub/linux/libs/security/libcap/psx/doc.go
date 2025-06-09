// Package psx provides support for system calls that are run
// simultaneously on all threads under Linux. It supports tool chains
// after go1.16. Earlier toolchains had no reliable way to support
// this because of [Bug 219478].
//
// The package works differently depending on whether or not
// CGO_ENABLED is 0 or 1.
//
// In the former case, psx is a low overhead wrapper for the two
// native go calls: syscall.AllThreadsSyscall() and
// syscall.AllThreadsSyscall6() introduced in go1.16. We provide this
// package wrapping to minimize client source code changes when
// compiling with or without CGo enabled.
//
// In the latter case it works via CGo wrappers for system call
// functions that call the C [lib]psx functions of these names. This
// ensures that the system calls execute simultaneously on all the
// threads of the Go (and CGo) combined runtime.
//
// With CGo, the psx support works in the following way: the thread
// that is first asked to execute the syscall does so, and determines
// if it succeeds or fails. If it fails, it returns immediately
// without attempting the syscall on other threads. If the initial
// attempt succeeds, however, then the runtime is stopped in order for
// the same system call to be performed on all the remaining threads
// of the runtime. Once all threads have completed the syscall, the
// return codes are those obtained by the first thread's invocation
// of the syscall.
//
// Note, there is no need to use this variant of syscall where the
// syscalls only read state from the kernel. However, since Go's
// runtime freely migrates code execution between threads, support of
// this type is required for any successful attempt to fully drop or
// modify the privilege of a running Go program under Linux.
//
// More info on how Linux privilege works and examples of using this
// package can be found on the [Fully Capable site].
//
// WARNING: For older go toolchains (prior to go1.16), the code should
// mostly work as far back as go1.11. However, like support for
// C.setuid(), this support is fragile and may hang. See the above bug
// for details.
//
// Copyright (c) 2019,20,24 Andrew G. Morgan <morgan@kernel.org>
//
// The psx package is licensed with a (you choose) BSD 3-clause or
// GPL2. See LICENSE file for details.
//
// [Bug 219478]: https://bugzilla.kernel.org/show_bug.cgi?id=219478
// [Fully Capable site]: https://sites.google.com/site/fullycapable
package psx // import "kernel.org/pub/linux/libs/security/libcap/psx"

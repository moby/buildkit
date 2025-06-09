# PSX

The Linux specific Go package,
`"kernel.org/pub/linux/libs/security/libcap/psx"`, provides an API for
invoking system calls in a way that each system call is mirrored on
all OS threads of the combined Go/CGo runtime. Since the Go runtime
treats OS threads as interchangeable, a feature like this is needed to
meaningfully change process privilege (including dropping privilege)
in a Go program running on Linux. This package is required by:

- `"kernel.org/pub/linux/libs/security/libcap/cap"`

When compiled `CGO_ENABLED=0`, the functionality requires go1.16+ to
build. That release of Go introduced `syscall.AllThreadsSyscall*()`
APIs.  When compiled this way, the PSX package functions
`psx.Syscall3()` and `psx.Syscall6()` are aliased to
`syscall.AllThreadsSyscall()` and `syscall.AllThreadsSyscall6()`
respectively.

When compiled `CGO_ENABLED=1`, the functionality is implemented by C
code, `[lib]psx`, which is distributed with `libcap`.

The official release announcements for libcap and libpsx are on the
[Fully Capable site](https://sites.google.com/site/fullycapable/).

Like `libcap/libpsx`, the "psx" package is distributed with a "you
choose" License. Specifically: BSD 3-clause, or GPL2. See the License
file.

Andrew G. Morgan <morgan@kernel.org>

//go:build !linux

package fs

import "golang.org/x/sys/unix"

const ENOATTR = unix.ENOATTR

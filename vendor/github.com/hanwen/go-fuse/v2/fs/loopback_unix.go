//go:build !freebsd

package fs

import (
	"bytes"
	"context"
	"syscall"

	"golang.org/x/sys/unix"
)

func retrieveAttrName(buf []byte) [][]byte {
	attributes := bytes.Split(buf, []byte{0})
	return attributes
}

var _ = (NodeListxattrer)((*LoopbackNode)(nil))

func (n *LoopbackNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	sz, err := unix.Llistxattr(n.path(), dest)
	return uint32(sz), ToErrno(err)
}

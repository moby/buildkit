//go:build !linux

package fuse

import (
	"golang.org/x/sys/unix"
)

// OSX and FreeBSD has races when multiple routines read
// from the FUSE device: on unmount, sometime some reads
// do not error-out, meaning that unmount will hang.
const useSingleReader = true

func (ms *Server) systemWrite(req *request, header []byte) Status {
	if req.flatDataSize() == 0 {
		err := handleEINTR(func() error {
			_, err := unix.Write(ms.mountFd, header)
			return err
		})
		return ToStatus(err)
	}

	if req.fdData != nil {
		sz := req.flatDataSize()
		buf := ms.allocOut(req, uint32(sz))
		req.flatData, req.status = req.fdData.Bytes(buf)
		header = req.serializeHeader(len(req.flatData))
	}

	_, err := writev(int(ms.mountFd), [][]byte{header, req.flatData})
	if req.readResult != nil {
		req.readResult.Done()
	}
	return ToStatus(err)
}

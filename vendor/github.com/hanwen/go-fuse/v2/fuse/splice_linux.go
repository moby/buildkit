// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"fmt"
	"log"
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/splice"
)

func (s *Server) setSplice() {
	s.canSplice = splice.Resizable() && !s.opts.DisableSplice
}

// trySplice: Zero-copy read from fdData.Fd into /dev/fuse
//
// Optimistic fast path: assumes fdData.Sz bytes are available (no short
// read). The caller has already serialized the reply header with that size.
// File data is copied into a pipe only once:
//
//  1. Write pre-serialized header into pipe  --> pipe: [header]
//  2. Splice file data directly into pipe    --> pipe: [header][payload]
//  3. Splice pipe into /dev/fuse
//
// If a short read occurs (payloadLen < fdData.Sz), the header in the pipe
// would carry the wrong total length, so we return an error and let the
// caller fall back to a Pread-based path.
func (r *fuseFD) trySplice(req *request, readResult ReadResult) error {
	// The caller (handleRequest) already called req.serializeHeader with
	// readResult.Size(), so req.outHeaderBuf is correct for the optimistic case.
	total := len(req.outHeaderBuf) + len(req.outDataBuf) + readResult.Size()

	pair, err := splice.Get()
	if err != nil {
		return err
	}
	defer splice.Done(pair)

	// Grow pipe to header + payload + one extra page.
	// Without the extra page the kernel will block once the pipe is almost full.
	if err := pair.Grow(total + os.Getpagesize()); err != nil {
		return err
	}

	// Write header into pipe.
	headerSz, err := writev(int(pair.WriteFd()), [][]byte{req.outHeaderBuf, req.outDataBuf})
	if err != nil {
		return err
	}
	if want := len(req.outHeaderBuf) + len(req.outDataBuf); headerSz != want {
		return fmt.Errorf("short write into splice: wrote %d, want %d", headerSz, want)
	}

	// Splice file data directly into pipe (single copy).
	var payloadLen int
	var fd uintptr
	var sz int
	var off int64
	if seekable, ok := readResult.(seekableResult); ok {
		fd, off, sz = seekable.Seekable()
		payloadLen, err = pair.LoadFromAt(fd, sz, off)
	} else if stateful, ok := readResult.(statefulResult); ok {
		fd, sz = stateful.Stateful()
		payloadLen, err = pair.LoadFrom(fd, sz)
	} else {
		return errRecoverSplice
	}

	if err != nil {
		return err
	}

	if payloadLen != sz {
		// Short read at EOF: the header carries the wrong total length.

		// drain header
		_, err := pair.Read(make([]byte, headerSz))
		if err != nil {
			return fmt.Errorf("fallback drain: %w", err)
		}

		if r.server.opts.Debug {
			log.Printf("tx %d:     OK fixup fd %db data", req.inHeader().Unique, payloadLen)
		}
		// New length.
		req.serializeHeader(payloadLen)

		return r.trySplice(req, ReadResultPipe(pair, payloadLen))
	}

	// Write header + payload to /dev/fuse.
	if cerr := r.withFD(func(fd int) {
		_, err = pair.WriteTo(uintptr(fd), total)
	}); cerr != nil {
		return cerr
	}
	return err
}

type pipeReadResult struct {
	pair *splice.Pair
	size int
}

func (r *pipeReadResult) Done() {
	splice.Done(r.pair)
	r.pair = nil
}

func (r *pipeReadResult) Bytes(buf []byte) ([]byte, Status) {
	n, err := r.pair.Read(buf)
	if n == -1 && err == syscall.EAGAIN {
		return nil, 0
	}
	return buf[:n], ToStatus(err)
}

func (r *pipeReadResult) Size() int {
	return r.size
}

func (r *pipeReadResult) Stateful() (fd uintptr, sz int) {
	return r.pair.ReadFd(), r.size
}

// ReadResultPipe returns a [ReadResult] of `size` bytes that was preloaded
// into the given pipe.  The pipe is discarded with splice.Done()
// after the read completes.
func ReadResultPipe(pipe *splice.Pair, size int) ReadResult {
	return &pipeReadResult{pipe, size}
}

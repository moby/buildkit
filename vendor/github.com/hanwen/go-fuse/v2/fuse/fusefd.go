// Copyright 2026 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// fuseFD owns the per-FUSE-fd state: the fd itself, the read-side
// bookkeeping (reqReaders, inflightRequestBytes), and the WaitGroup
// tracking reader goroutines on this fd. The pools are shared with the
// owning Server via pointers, and a back pointer to the Server gives the
// per-fd methods access to shared configuration and callbacks.
//
// Today a Server has a single fuseFD; this struct exists so that adding
// support for FUSE_DEV_IOC_CLONE'd fds is a matter of widening the field
// to a slice.
type fuseFD struct {
	server *Server

	// I/O with kernel and daemon. file owns the FUSE fd; conn is its
	// RawConn. Running syscalls through conn holds a reference on the
	// fd, so concurrent writers and close() are safe against the fd
	// number being reused without any explicit locking.
	file *os.File
	conn syscall.RawConn

	reqMu                sync.Mutex
	reqReaders           int
	inflightRequestBytes int

	// loops tracks reader goroutines servicing fd.
	loops sync.WaitGroup

	// Shared pools owned by Server.
	reqPool  *sync.Pool
	readPool *sync.Pool
	buffers  *bufferPool

	// Accounting constants, set once at server construction.
	reqAllocBytes int
	readBufBytes  int
}

// newFuseFD returns a fuseFD bound to ms and ready to read from fd.
// Ownership of fd passes to the returned fuseFD's *os.File. All shared
// state (pools, buffer pool, accounting constants) is wired up here.
func (ms *Server) newFuseFD(fd int) (*fuseFD, error) {
	file := os.NewFile(uintptr(fd), "/dev/fuse")
	conn, err := file.SyscallConn()
	if err != nil {
		file.Close()
		return nil, err
	}
	_, readBufBytes, reqAllocBytes := requestAccountingSizes(ms.opts.MaxWrite)
	return &fuseFD{
		server:        ms,
		file:          file,
		conn:          conn,
		reqPool:       &ms.reqPool,
		readPool:      &ms.readPool,
		buffers:       &ms.buffers,
		reqAllocBytes: reqAllocBytes,
		readBufBytes:  readBufBytes,
	}, nil
}

// withFD runs f with the underlying FUSE file descriptor, holding a
// reference on it for the duration so the fd cannot be closed (and its
// number reused) while f runs. This is what lets concurrent writers and
// close() run without a serializing mutex. The error is non-nil only
// when the fd is already closed, in which case f is not called.
func (r *fuseFD) withFD(f func(fd int)) error {
	return r.conn.Control(func(fd uintptr) {
		f(int(fd))
	})
}

// writevFD writes iov to the FUSE fd, holding a reference against
// concurrent close. The error is the underlying syscall error, or a
// non-syscall error if the fd is already closed.
func (r *fuseFD) writevFD(iov [][]byte) (int, error) {
	var n int
	var err error
	if cerr := r.withFD(func(fd int) {
		n, err = writev(fd, iov)
	}); cerr != nil {
		return 0, cerr
	}
	return n, err
}

// readRequest reads one request from the kernel. Returns nil, OK if
// there are too many concurrent readers or insufficient request-bytes
// budget.
func (r *fuseFD) readRequest() (req *requestAlloc, code Status) {
	ms := r.server
	r.reqMu.Lock()
	if r.reqReaders > ms.maxReaders || !r.reserveRequestBytes() {
		r.reqMu.Unlock()
		return nil, OK
	}
	r.reqReaders++
	r.reqMu.Unlock()

	req = r.reqPool.Get().(*requestAlloc)
	dest := r.readPool.Get().([]byte)

	var n int
	err := handleEINTR(func() error {
		var err error
		if cerr := r.withFD(func(fd int) {
			n, err = syscall.Read(fd, dest)
		}); cerr != nil {
			return cerr
		}
		return err
	})
	if err != nil {
		r.reqMu.Lock()
		r.putReadBuf(dest)
		r.putReq(req)
		r.reqReaders--
		r.reqMu.Unlock()
		return nil, ToStatus(err)
	}

	r.reqMu.Lock()
	defer r.reqMu.Unlock()
	gobbled := req.setInput(dest[:n])
	if len(req.inputBuf) < int(unsafe.Sizeof(InHeader{})) {
		log.Printf("Short read for input header: %v", req.inputBuf)
		r.putReadBuf(dest)
		r.putReq(req)
		r.reqReaders--
		return nil, EINVAL
	}
	opCode := ((*InHeader)(unsafe.Pointer(&req.inputBuf[0]))).Opcode
	/* These messages don't expect reply, so they cost nothing for
	   the kernel to send. Make sure we're not overwhelmed by not
	   spawning a new reader.
	*/
	needsBackPressure := (opCode == _OP_FORGET || opCode == _OP_BATCH_FORGET)

	if !gobbled {
		r.putReadBuf(dest)
	}
	r.reqReaders--
	if !ms.singleReader && r.reqReaders <= 0 && !needsBackPressure {
		r.loops.Add(1)
		go ms.loop()
	}

	return req, OK
}

// returnRequest returns a request to the pool of unused requests.
func (r *fuseFD) returnRequest(req *requestAlloc) {
	if req.bufferPoolOutputBuf != nil {
		r.buffers.FreeBuffer(req.bufferPoolOutputBuf)
		req.bufferPoolOutputBuf = nil
	}
	if req.interrupted {
		req.interrupted = false
		req.cancel = make(chan struct{}, 0)
	}
	req.clear()

	r.reqMu.Lock()
	if p := req.bufferPoolInputBuf; p != nil {
		req.bufferPoolInputBuf = nil
		r.putReadBuf(p)
	}
	r.putReq(req)
	r.reqMu.Unlock()
}

func (r *fuseFD) reserveRequestBytes() bool {
	if !r.canReserveRequestBytes() {
		return false
	}
	r.inflightRequestBytes += r.requestBytes()
	return true
}

func (r *fuseFD) canReserveRequestBytes() bool {
	return r.inflightRequestBytes == 0 ||
		r.requestBytes() <= r.server.opts.MaxInflightRequestBytes-r.inflightRequestBytes
}

// canAcceptAnother wraps canReserveRequestBytes with reqMu, for callers
// that don't already hold the lock.
func (r *fuseFD) canAcceptAnother() bool {
	r.reqMu.Lock()
	defer r.reqMu.Unlock()
	return r.canReserveRequestBytes()
}

func (r *fuseFD) requestBytes() int {
	return r.reqAllocBytes + r.readBufBytes
}

func (r *fuseFD) putReadBuf(buf []byte) {
	r.readPool.Put(buf)
	r.inflightRequestBytes -= r.readBufBytes
}

func (r *fuseFD) putReq(req *requestAlloc) {
	r.reqPool.Put(req)
	r.inflightRequestBytes -= r.reqAllocBytes
}

// close closes the underlying FUSE fd. The *os.File waits for in-flight
// RawConn operations (reads, writes, ioctls) before releasing the fd.
func (r *fuseFD) close() error {
	return r.file.Close()
}

func (r *fuseFD) writev(iov [][]byte) (int, syscall.Errno) {
	n, err := r.writevFD(iov)
	if err == nil {
		return n, 0
	}
	errno, ok := err.(syscall.Errno)
	if !ok {
		// The fd is closed; report it as such.
		return n, syscall.EBADF
	}
	if errno == syscall.EINVAL {
		// Detail: the kernel returns EINVAL for unsupported
		// notify methods.
		errno = syscall.ENOSYS
	}
	return n, errno
}

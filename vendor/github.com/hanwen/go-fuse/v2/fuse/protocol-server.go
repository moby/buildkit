// Copyright 2024 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"sync"
	"syscall"
)

// protocolServer bridges from the FUSE datatypes to a RawFileSystem
type protocolServer struct {
	fileSystem RawFileSystem

	writev func([][]byte) (int, syscall.Errno)

	interruptMu    sync.Mutex
	reqInflight    []*request
	connectionDead bool

	kernelSettings InitIn

	opts *MountOptions

	// in-flight notify-retrieve queries
	retrieveMu   sync.Mutex
	retrieveNext uint64
	retrieveTab  map[uint64]*retrieveCacheRequest // notifyUnique -> retrieve request
}

func (ms *protocolServer) handleRequest(h *operationHandler, req *request) {
	ms.addInflight(req)
	defer ms.dropInflight(req)

	if req.status.Ok() && ms.opts.Debug {
		ms.opts.Logger.Println(req.InputDebug())
	}

	if h == nil || h.Func == nil {
		c := req.inHeader().Opcode
		if c != _OP_COPY_FILE_RANGE_64 { // _OP_COPY_FILE_RANGE_64 is intentionally not supported.
			ms.opts.Logger.Printf("Unimplemented opcode %v", operationName(c))
		}
		req.status = ENOSYS
	} else if req.inHeader().NodeId == pollHackInode ||
		req.inHeader().NodeId == FUSE_ROOT_ID && h.FileNames > 0 && req.filename() == pollHackName {
		doPollHackLookup(ms, req)
	} else if req.status.Ok() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					req.status = ms.opts.PanicHandler(r)
					if req.status == 0 {
						req.status = EIO
					}
				}
			}()
			h.Func(ms, req)
		}()
	}

	// Forget/NotifyReply do not wait for reply from filesystem server.
	switch req.inHeader().Opcode {
	case _OP_INTERRUPT:
		// ? what other status can interrupt generate?
		if req.status.Ok() {
			req.suppressReply = true
		}
	default:
		req.suppressReply = h != nil && h.SuppressReply
	}
	if req.status == EINTR {
		ms.interruptMu.Lock()
		dead := ms.connectionDead
		ms.interruptMu.Unlock()
		if dead {
			req.suppressReply = true
		}
	}
	if req.suppressReply {
		return
	}
	if req.readResult != nil && ms.opts.DisableSplice {
		req.outPayload, req.status = req.readResult.Bytes(req.outPayload)
		req.readResult.Done()
		req.readResult = nil
	}

	req.serializeHeader(req.outPayloadSize())

	if ms.opts.Debug {
		ms.opts.Logger.Println(req.OutputDebug())
	}
}

func (ms *protocolServer) addInflight(req *request) {
	ms.interruptMu.Lock()
	defer ms.interruptMu.Unlock()
	req.inflightIndex = len(ms.reqInflight)
	ms.reqInflight = append(ms.reqInflight, req)
}

func (ms *protocolServer) dropInflight(req *request) {
	ms.interruptMu.Lock()
	defer ms.interruptMu.Unlock()
	this := req.inflightIndex
	last := len(ms.reqInflight) - 1
	if last != this {
		ms.reqInflight[this] = ms.reqInflight[last]
		ms.reqInflight[this].inflightIndex = this
	}
	ms.reqInflight = ms.reqInflight[:last]
}

func (ms *protocolServer) interruptRequest(unique uint64) Status {
	ms.interruptMu.Lock()
	defer ms.interruptMu.Unlock()

	// This is slow, but this operation is rare.
	for _, inflight := range ms.reqInflight {
		if unique == inflight.inHeader().Unique && !inflight.interrupted {
			close(inflight.cancel)
			inflight.interrupted = true
			return OK
		}
	}

	return EAGAIN
}

func (ms *protocolServer) cancelAll() {
	ms.interruptMu.Lock()
	defer ms.interruptMu.Unlock()
	ms.connectionDead = true
	for _, req := range ms.reqInflight {
		if !req.interrupted {
			close(req.cancel)
			req.interrupted = true
		}
	}
	// Leave ms.reqInflight alone, or dropInflight will barf.
}

// ProtocolServer bridges from FUSE request/response types to the
// Go-FUSE RawFileSystem API calls.
//
// EXPERIMENTAL: not subject to API stability.
type ProtocolServer struct {
	protocolServer
}

// NewProtocolServer creates a ProtocolServer for the RawFileSystem.
//
// EXPERIMENTAL: not subject to API stability.
func NewProtocolServer(fs RawFileSystem, opts *MountOptions) *ProtocolServer {
	// ProtocolServer has no pipe, so splicing READ results to the
	// caller is not possible; force the in-process READ path.
	optsCopy := *opts
	optsCopy.setDefaults(fs)
	optsCopy.DisableSplice = true

	return &ProtocolServer{
		protocolServer: protocolServer{
			fileSystem:  fs,
			retrieveTab: make(map[uint64]*retrieveCacheRequest),
			opts:        &optsCopy,
		},
	}
}

func iovLen(iov [][]byte) int {
	var r int
	for _, e := range iov {
		r += len(e)
	}
	return r
}

// HandleRequest parses the iov in `in`, calls into the raw
// filesystem, and puts the result in `out`. The shapes of the
// input/output IOVs should follow conventions used by virtiofs.
// The return value is the number of bytes written.
//
// EXPERIMENTAL: not subject to API stability.
func (ps *ProtocolServer) HandleRequest(in [][]byte, out [][]byte) (int, Status) {
	// for virtiofs, we get
	//
	// 2026/04/17 13:34:40 in: 40 32
	// 2026/04/17 13:34:40 out: 16 16 4096
	//
	// ie. the iov looks like {header , variable size, payload},
	// for both input and output.
	//
	// Our input data types have the InHeader embedded in the FooIn
	// types, so we can never fully avoid copying.
	inTogether := make([]byte, iovLen(in[:min(2, len(in))]))
	copy(inTogether, in[0])
	if len(in) > 1 {
		copy(inTogether[len(in[0]):], in[1])
	}
	h, inSize, outSize, outPayloadSize, errno := parseRequest(inTogether, &ps.kernelSettings)
	if errno != 0 {
		return 0, errno
	}
	req := request{
		cancel:        make(chan struct{}),
		inputBuf:      inTogether[:inSize],
		suppressReply: h.SuppressReply,
	}

	if len(in) > 2 {
		req.inPayload = in[2]
	} else {
		req.inPayload = inTogether[inSize:]
	}

	startOut := out
	if !h.SuppressReply {
		// validate the shape of the output iov. If we fail any of this, it's probably a programming error on our side, but
		// since we can't trust the guest, be paranoid and return EIO instead.
		if len(out) > 0 && len(out[0]) == int(sizeOfOutHeader) {
			req.outHeaderBuf = out[0]
			out = out[1:]
		} else {
			ps.opts.Logger.Printf("op %v: got %v, out iov should start with 16 bytes", h.Name, iovLens(startOut))
			return 0, EIO
		}

		if outSize > 0 {
			if len(out) > 0 && len(out[0]) == outSize {
				req.outDataBuf = out[0]
				out = out[1:]
			} else {
				ps.opts.Logger.Printf("op %v: got %v, outData iov should have %d bytes", h.Name, iovLens(startOut), outSize)
				return 0, EIO
			}
		}

		if len(out) > 0 {
			if len(out[0]) < outPayloadSize {
				ps.opts.Logger.Printf("op %s: got %v, payload iov should have %d bytes", h.Name, iovLens(startOut), outPayloadSize)
				return 0, EIO
			}
			req.outPayload = out[0]
			out = out[1:]
		} else if outPayloadSize != 0 {
			ps.opts.Logger.Printf("op %s: got %v, payload iov should have %d bytes", h.Name, iovLens(startOut), outPayloadSize)
			return 0, EIO
		}
	}

	beforePayload := req.outPayload
	ps.protocolServer.handleRequest(h, &req)
	if len(req.outPayload) > 0 && len(beforePayload) > 0 &&
		&beforePayload[0] != &req.outPayload[0] {
		n := copy(beforePayload, req.outPayload)
		req.outPayload = beforePayload[:n]
	}

	// Per the virtio spec, vring_used_elem.len should hold the
	// number of bytes the device wrote to the descriptor
	// chain. Returning more  inflates the live-migration dirty-page
	// log and violates the spec.
	return int(sizeOfOutHeader) + len(req.outDataBuf) + len(req.outPayload), 0
}

func iovLens(in [][]byte) []int {
	var lens []int
	for _, b := range in {
		lens = append(lens, len(b))
	}
	return lens
}

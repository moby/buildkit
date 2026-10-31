// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"bytes"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"
	"unsafe"
)

const sizeOfOutHeader = unsafe.Sizeof(OutHeader{})

type request struct {
	inflightIndex int

	cancel chan struct{}

	suppressReply bool

	// written under Server.interruptMu
	interrupted bool

	// inHeader + opcode specific data
	inputBuf []byte

	// Output header (OutHeader).
	outHeaderBuf []byte

	// Opcode-specific structured output data.
	outDataBuf []byte

	// Unstructured input (filenames, data for WRITE call)
	inPayload []byte

	// Output data.
	status Status

	// Unstructured output. Only one of these is non-nil.
	outPayload []byte
	readResult ReadResult

	// Start timestamp for timing info.
	startTime time.Time
}

// requestAlloc holds the request, plus I/O buffers, which are
// reused across requests.
type requestAlloc struct {
	request

	// Request storage. For large inputs and outputs, use data
	// obtained through bufferpool.
	bufferPoolInputBuf  []byte
	bufferPoolOutputBuf []byte

	// For small pieces of data, we use the following inline arrays:

	// Fixed-size output header storage.
	outHeaderInline [unsafe.Sizeof(OutHeader{})]byte

	// Opcode-specific structured output data storage.
	outDataInline [uintptr(outputDataSize)]byte

	// Input, if small enough to fit here.
	smallInputBuf [128]byte
}

func (r *request) inHeader() *InHeader {
	return (*InHeader)(r.inData())
}

func (r *request) outHeader() *OutHeader {
	return (*OutHeader)(unsafe.Pointer(&r.outHeaderBuf[0]))
}

// TODO - benchmark to see if this is necessary?
func (r *request) clear() {
	r.suppressReply = false
	r.inputBuf = nil
	r.outHeaderBuf = nil
	r.outDataBuf = nil
	r.inPayload = nil
	r.status = OK
	r.outPayload = nil
	r.startTime = time.Time{}
	r.readResult = nil
}

func asType(ptr unsafe.Pointer, typ any) any {
	return reflect.NewAt(reflect.ValueOf(typ).Type(), ptr).Interface()
}

func typSize(typ any) uintptr {
	return reflect.ValueOf(typ).Type().Size()
}

func (r *request) InputDebug() string {
	val := ""

	hdr := r.inHeader()
	h := getHandler(hdr.Opcode)
	if h != nil && h.InType != nil {
		val = Print(asType(r.inData(), h.InType))
	}

	names := ""
	if h.FileNames == 1 {
		name := r.filename()

		rest := r.inPayload[len(name)+1:]
		names = fmt.Sprintf(" %q %s", name, summarizePayload(rest))
	} else if h.FileNames == 2 {
		n1, n2 := r.filenames()
		names = fmt.Sprintf(" %q %q", n1, n2)
	} else {
		names = summarizePayload(r.inPayload)
	}

	return fmt.Sprintf("rx %d: %s n%d %s%s p%d",
		hdr.Unique, operationName(hdr.Opcode), hdr.NodeId,
		val, names, hdr.Caller.Pid)
}

func summarizePayload(p []byte) string {
	l := len(p)
	if l > 0 {
		dots := ""
		if l > 8 {
			l = 8
			dots = "..."
		}

		return fmt.Sprintf("%q%s %db", p[:l], dots, len(p))
	}
	return ""
}

func (r *request) OutputDebug() string {
	var dataStr string
	h := getHandler(r.inHeader().Opcode)
	if h != nil && h.OutType != nil && len(r.outDataBuf) > 0 {
		dataStr = Print(asType(r.outData(), h.OutType))
	}

	max := 1024
	if len(dataStr) > max {
		dataStr = dataStr[:max] + " ...trimmed"
	}

	flatStr := ""
	if r.outPayloadSize() > 0 {
		if h != nil && h.FileNameOut {
			s := strings.TrimRight(string(r.outPayload), "\x00")
			flatStr = fmt.Sprintf(" %q", s)
		} else {
			spl := ""

			if r.readResult != nil {
				_, pipeOK := r.readResult.(statefulResult)
				_, fdOK := r.readResult.(seekableResult)
				if fdOK || pipeOK {
					spl = fmt.Sprintf(" (fd %db data)", r.readResult.Size())
				}
			} else {
				l := len(r.outPayload)
				s := ""
				if l > 8 {
					l = 8
					s = "..."
				}
				spl = fmt.Sprintf(" %q%s", r.outPayload[:l], s)
			}
			flatStr = fmt.Sprintf(" %db data%s", r.outPayloadSize(), spl)
		}
	}

	extraStr := dataStr + flatStr
	if extraStr != "" {
		extraStr = ", " + extraStr
	}
	return fmt.Sprintf("tx %d:     %v%s",
		r.inHeader().Unique, r.status, extraStr)
}

// setInput returns true if it takes ownership of the argument, false if not.
func (r *requestAlloc) setInput(input []byte) bool {
	if len(input) < len(r.smallInputBuf) {
		copy(r.smallInputBuf[:], input)
		r.inputBuf = r.smallInputBuf[:len(input)]
		return false
	}
	r.inputBuf = input
	r.bufferPoolInputBuf = input[:cap(input)]

	return true
}

func (r *request) inData() unsafe.Pointer {
	return unsafe.Pointer(&r.inputBuf[0])
}

// note: outSize is without OutHeader
func parseRequest(in []byte, kernelSettings *InitIn) (h *operationHandler, inSize, outSize, outPayloadSize int, errno Status) {
	inSize = int(unsafe.Sizeof(InHeader{}))
	if len(in) < inSize {
		errno = EIO
		return
	}
	inData := unsafe.Pointer(&in[0])
	hdr := (*InHeader)(inData)
	h = getHandler(hdr.Opcode)
	if h == nil {
		errno = ENOSYS
		return
	}
	if h.InputSize > 0 {
		inSize = int(h.InputSize)
	}
	if hdr.Opcode == _OP_RENAME && kernelSettings.supportsRenameSwap() {
		inSize = int(unsafe.Sizeof(RenameIn{}))
	}
	if hdr.Opcode == _OP_INIT && inSize > len(in) {
		// Minor version 36 extended the size of InitIn struct
		inSize = len(in)
	}
	if len(in) < inSize {
		log.Printf("Short read for %v: %q", h.Name, in)
		errno = EIO
		return
	}

	outSize = int(h.OutputSize)

	switch hdr.Opcode {
	case _OP_READDIR, _OP_READDIRPLUS, _OP_READ:
		outPayloadSize = int(((*ReadIn)(inData)).Size)
	case _OP_GETXATTR, _OP_LISTXATTR:
		// [GET|LIST]XATTR is two opcodes in one: get/list xattr size (return
		// structured GetXAttrOut, no flat data) and get/list xattr data
		// (return no structured data, but only flat data)
		outPayloadSize = int(((*GetXAttrIn)(inData)).Size)
		if outPayloadSize > 0 {
			outSize = 0
		}
	case _OP_IOCTL:
		outPayloadSize = int(((*IoctlIn)(inData)).OutSize)
	}

	return
}

func (r *request) outData() unsafe.Pointer {
	return unsafe.Pointer(&r.outDataBuf[0])
}

func (r *request) filename() string {
	idx := bytes.IndexByte(r.inPayload, 0)

	name := r.inPayload
	if idx >= 0 {
		name = name[:idx]
	}
	return string(name)
}

func (r *request) filenames() (string, string) {
	i1 := bytes.IndexByte(r.inPayload, 0)
	if i1 < 0 || i1+1 >= len(r.inPayload) {
		return "", ""
	}
	s1 := string(r.inPayload[:i1])
	s2 := string(r.inPayload[i1+1 : len(r.inPayload)-1])
	return s1, s2
}

// serializeHeader serializes the response header. The header points
// to an internal buffer of the receiver.
func (r *request) serializeHeader(outPayloadSize int) {
	if r.status > OK {
		// only do this for positive status; negative status
		// is used for notification.
		r.outDataBuf = nil
		outPayloadSize = 0
	}

	// The InitOut structure has 24 bytes (ie. TimeGran and
	// further fields not available) in fuse version <= 22.
	// https://john-millikin.com/the-fuse-protocol#FUSE_INIT
	if r.status.Ok() && r.inHeader().Opcode == _OP_INIT {
		out := (*InitOut)(r.outData())
		if out.Minor <= 22 {
			r.outDataBuf = r.outDataBuf[:24]
		}
	}

	o := r.outHeader()
	o.Unique = r.inHeader().Unique
	o.Status = int32(-r.status)
	o.Length = uint32(
		int(sizeOfOutHeader) + len(r.outDataBuf) + outPayloadSize)

	if r.outPayload != nil {
		r.outPayload = r.outPayload[:outPayloadSize]
	}
}

func (r *request) outPayloadSize() int {
	if r.readResult != nil {
		return r.readResult.Size()
	}
	return len(r.outPayload)
}

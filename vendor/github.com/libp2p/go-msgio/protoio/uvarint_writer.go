//
// Adapted from gogo/protobuf to use multiformats/go-varint for
// efficient, interoperable length-prefixing.
//
// Protocol Buffers for Go with Gadgets
//
// Copyright (c) 2013, The GoGo Authors. All rights reserved.
// http://github.com/gogo/protobuf
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//     * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//     * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
package protoio

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/gogo/protobuf/proto"

	"github.com/multiformats/go-varint"
)

type uvarintWriter struct {
	w      io.Writer
	lenBuf []byte
	buffer []byte
}

func NewDelimitedWriter(w io.Writer) WriteCloser {
	return &uvarintWriter{w, make([]byte, varint.MaxLenUvarint63), nil}
}

func (uw *uvarintWriter) WriteMsg(msg proto.Message) (err error) {
	defer func() {
		if rerr := recover(); rerr != nil {
			fmt.Fprintf(os.Stderr, "caught panic: %s\n%s\n", rerr, debug.Stack())
			err = fmt.Errorf("panic reading message: %s", rerr)
		}
	}()

	var data []byte
	if m, ok := msg.(interface {
		MarshalTo(data []byte) (n int, err error)
	}); ok {
		n, ok := getSize(m)
		if ok {
			if n+varint.MaxLenUvarint63 >= len(uw.buffer) {
				uw.buffer = make([]byte, n+varint.MaxLenUvarint63)
			}
			lenOff := varint.PutUvarint(uw.buffer, uint64(n))
			_, err = m.MarshalTo(uw.buffer[lenOff:])
			if err != nil {
				return err
			}
			_, err = uw.w.Write(uw.buffer[:lenOff+n])
			return err
		}
	}

	// fallback
	data, err = proto.Marshal(msg)
	if err != nil {
		return err
	}
	length := uint64(len(data))
	n := varint.PutUvarint(uw.lenBuf, length)
	_, err = uw.w.Write(uw.lenBuf[:n])
	if err != nil {
		return err
	}
	_, err = uw.w.Write(data)
	return err
}

func (uw *uvarintWriter) Close() error {
	if closer, ok := uw.w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

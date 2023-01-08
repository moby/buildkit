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
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/gogo/protobuf/proto"

	"github.com/multiformats/go-varint"
)

type uvarintReader struct {
	r       *bufio.Reader
	buf     []byte
	maxSize int
	closer  io.Closer
}

func NewDelimitedReader(r io.Reader, maxSize int) ReadCloser {
	var closer io.Closer
	if c, ok := r.(io.Closer); ok {
		closer = c
	}
	return &uvarintReader{bufio.NewReader(r), nil, maxSize, closer}
}

func (ur *uvarintReader) ReadMsg(msg proto.Message) (err error) {
	defer func() {
		if rerr := recover(); rerr != nil {
			fmt.Fprintf(os.Stderr, "caught panic: %s\n%s\n", rerr, debug.Stack())
			err = fmt.Errorf("panic reading message: %s", rerr)
		}
	}()

	length64, err := varint.ReadUvarint(ur.r)
	if err != nil {
		return err
	}
	length := int(length64)
	if length < 0 || length > ur.maxSize {
		return io.ErrShortBuffer
	}
	if len(ur.buf) < length {
		ur.buf = make([]byte, length)
	}
	buf := ur.buf[:length]
	if _, err := io.ReadFull(ur.r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, msg)
}

func (ur *uvarintReader) Close() error {
	if ur.closer != nil {
		return ur.closer.Close()
	}
	return nil
}

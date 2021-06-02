// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build !windows

package jaeger

import (
	"net"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func assertSockBufferSize(t *testing.T, expectedBytes int, conn *net.UDPConn) bool {
	fd, err := conn.File()
	if !assert.NoError(t, err) {
		return false
	}

	bufferBytes, err := syscall.GetsockoptInt(int(fd.Fd()), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	if !assert.NoError(t, err) {
		return false
	}

	// The linux kernel doubles SO_SNDBUF value (to allow space for
	// bookkeeping overhead) when it is set using setsockopt(2), and this
	// doubled value is returned by getsockopt(2)
	// https://linux.die.net/man/7/socket
	if runtime.GOOS == "linux" {
		return assert.GreaterOrEqual(t, expectedBytes*2, bufferBytes)
	}

	return assert.Equal(t, expectedBytes, bufferBytes)
}

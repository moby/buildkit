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

// +build windows

package jaeger

import (
	"net"
	"testing"
)

func assertSockBufferSize(t *testing.T, expectedBytes int, conn *net.UDPConn) bool {
	// The Windows implementation of the net.UDPConn does not implement the
	// functionality to return a file handle, instead a "not supported" error
	// is returned:
	//
	// https://github.com/golang/go/blob/6cc8aa7ece96aca282db19f08aa5c98ed13695d9/src/net/fd_windows.go#L175-L178
	//
	// This means we are not able to pass the connection to a syscall and
	// determine the buffer size.
	return true
}

// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import "unsafe"

const _OP_MONITOR = uint32(60)

func doMonitor(_ *protocolServer, req *request) {
	// macFUSE sends watcher-monitor notifications for files and directories.
	// They are advisory and do not require a reply.
	req.suppressReply = true
}

func init() {
	operationHandlers[_OP_MONITOR] = &operationHandler{
		Name:      "MONITOR",
		Func:      doMonitor,
		InputSize: unsafe.Sizeof(MonitorIn{}),
		InType:    MonitorIn{},
	}

	checkFixedBufferSize()
}

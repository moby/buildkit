// Copyright 2016 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fuse

import (
	"runtime"
	"strings"
	"syscall"
)

func init() {
	// syscall.O_LARGEFILE is 0x0 on x86_64, but the kernel
	// supplies 0x8000 anyway, except on mips64el, where 0x8000 is
	// used for O_DIRECT.
	if !strings.Contains(runtime.GOARCH, "mips64") {
		openFlagNames.set(0x8000, "LARGEFILE")
	}

	openFlagNames.set(syscall.O_DIRECT, "DIRECT")
	openFlagNames.set(syscall_O_NOATIME, "NOATIME")
	initFlagNames.set(CAP_NO_OPENDIR_SUPPORT, "NO_OPENDIR_SUPPORT")
	initFlagNames.set(CAP_EXPLICIT_INVAL_DATA, "EXPLICIT_INVAL_DATA")
	initFlagNames.set(CAP_MAP_ALIGNMENT, "MAP_ALIGNMENT")
	initFlagNames.set(CAP_SUBMOUNTS, "SUBMOUNTS")
	initFlagNames.set(CAP_HANDLE_KILLPRIV_V2, "HANDLE_KILLPRIV_V2")
	initFlagNames.set(CAP_SETXATTR_EXT, "SETXATTR_EXT")
	initFlagNames.set(CAP_INIT_EXT, "INIT_EXT")
	initFlagNames.set(CAP_INIT_RESERVED, "INIT_RESERVED")
}

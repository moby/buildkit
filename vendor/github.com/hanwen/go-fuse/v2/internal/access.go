// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package internal

import (
	"os/user"
	"slices"
	"strconv"
	"syscall"
)

// HasAccess tests if a caller can access a file with permissions
// `perm` in mode `mask`. This follows Unix semantics: the caller is
// classified as owner, group member or other, and only the
// permission bits of that single class are consulted. All bits in
// mask must be granted.
func HasAccess(callerUid, callerGid, fileUid, fileGid uint32, perm uint32, mask uint32) bool {
	mask &= 7
	if mask == 0 {
		return true
	}
	if callerUid == 0 {
		// Root can read and write anything, but for execute, at
		// least one of the execute bits must be set, unless it is a
		// directory.
		return mask&1 == 0 || perm&0111 != 0 || perm&syscall.S_IFMT == syscall.S_IFDIR
	}

	var bits uint32
	switch {
	case callerUid == fileUid:
		bits = perm >> 6
	case callerGid == fileGid:
		bits = perm >> 3
	case (perm>>3)&mask != perm&mask && isSupplementaryGroup(callerUid, fileGid):
		// The expensive group lookup only matters if group and other
		// bits differ for the requested mask.
		bits = perm >> 3
	default:
		bits = perm
	}
	return bits&mask == mask
}

func isSupplementaryGroup(callerUid, fileGid uint32) bool {
	u, err := user.LookupId(strconv.Itoa(int(callerUid)))
	if err != nil {
		return false
	}
	gs, err := u.GroupIds()
	if err != nil {
		return false
	}
	return slices.Contains(gs, strconv.Itoa(int(fileGid)))
}

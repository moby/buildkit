// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
)

type dirArray struct {
	entries []fuse.DirEntry
}

func (a *dirArray) HasNext() bool {
	return len(a.entries) > 0
}

func (a *dirArray) Next() (fuse.DirEntry, syscall.Errno) {
	e := a.entries[0]
	a.entries = a.entries[1:]
	return e, 0
}

func (a *dirArray) Close() {

}

// NewListDirStream wraps a slice of DirEntry as a DirStream.
func NewListDirStream(list []fuse.DirEntry) DirStream {
	return &dirArray{list}
}

// implement FileReaddirenter/FileReleasedirer
type dirStreamAsFile struct {
	creator func(context.Context) (DirStream, syscall.Errno)
	ds      DirStream
}

func (d *dirStreamAsFile) Releasedir(ctx context.Context, releaseFlags uint32) {
	if d.ds != nil {
		d.ds.Close()
	}
}

func (d *dirStreamAsFile) Readdirent(ctx context.Context) (de *fuse.DirEntry, errno syscall.Errno) {
	if d.ds == nil {
		d.ds, errno = d.creator(ctx)
		if errno != 0 {
			return nil, errno
		}
	}
	if !d.ds.HasNext() {
		return nil, 0
	}

	e, errno := d.ds.Next()
	return &e, errno
}

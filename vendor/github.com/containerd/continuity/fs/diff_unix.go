// +build !windows

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package fs

import (
	"bytes"
	"os"
	"strings"
	"syscall"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/continuity/sysx"
	"github.com/pkg/errors"
)

// deleteChangeOverlay checks if file has been deleted 
// in overlay fs by checking if file is marked as char device.
func deleteChangeOverlay(diffDir, path string, f os.FileInfo) (string, error) {
	if f.Mode()&os.ModeCharDevice != 0 {
		return path, nil
	} else {
		return "", nil
	}
}

// getLowerDir returns the list of lower directories from
// the options of overlay mount, provided no upperdir 
// exists ( implying readonly overaly)
func getLowerDir(vfsOptions string) ([]string) {
	options := strings.Split(vfsOptions, ",")
	var lowerDir []string
	for _, opts := range options {
                if strings.HasPrefix(opts, "lowerdir=") {
			lowerDir = strings.Split(strings.Split(opts, "lowerdir=")[1], ":")
                }
                if strings.HasPrefix(opts, "upperdir=") {
                        return  []string{}
                }
	}
	return lowerDir
}

// detectDirDiff returns diff dir options if a directory could
// be found in the mount info for upper which is the direct
// diff with the provided lower directory
func detectDirDiff(upper, lower string) *diffDirOptions {
	// TODO: detect AUFS
	// Implemented only for overlay
	mountInfoUpper, err := mount.Lookup(upper)
	if err != nil {
		return nil
	}
	mountInfoLower, err := mount.Lookup(lower)
	if err != nil {
		return nil
	}
	if mountInfoUpper.FSType != "overlay" {
		return nil
	}
	if mountInfoLower.FSType != "overlay" {
		return nil
	}
	upperLowerDir := getLowerDir(mountInfoUpper.VFSOptions)
	lowerLowerDir := getLowerDir(mountInfoLower.VFSOptions)
	if len(lowerLowerDir) != len(upperLowerDir) - 1 {
		// the mount points differ by more than 1 snapshots.
		// In this case resort to double diff walk between the 
		// mount points.
		return nil
	}
	for i, dir := range lowerLowerDir {
		if upperLowerDir[i+1] != dir {
			// the lower dirs are not in order. So compare with 
			// double diff walk.
			return nil
		}
	}

	var options diffDirOptions
	options.diffDir = upperLowerDir[0]
	options.deleteChange = deleteChangeOverlay

	return  &options
}

// compareSysStat returns whether the stats are equivalent,
// whether the files are considered the same file, and
// an error
func compareSysStat(s1, s2 interface{}) (bool, error) {
	ls1, ok := s1.(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	ls2, ok := s2.(*syscall.Stat_t)
	if !ok {
		return false, nil
	}

	return ls1.Mode == ls2.Mode && ls1.Uid == ls2.Uid && ls1.Gid == ls2.Gid && ls1.Rdev == ls2.Rdev, nil
}

func compareCapabilities(p1, p2 string) (bool, error) {
	c1, err := sysx.LGetxattr(p1, "security.capability")
	if err != nil && err != sysx.ENODATA {
		return false, errors.Wrapf(err, "failed to get xattr for %s", p1)
	}
	c2, err := sysx.LGetxattr(p2, "security.capability")
	if err != nil && err != sysx.ENODATA {
		return false, errors.Wrapf(err, "failed to get xattr for %s", p2)
	}
	return bytes.Equal(c1, c2), nil
}

func isLinked(f os.FileInfo) bool {
	s, ok := f.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return !f.IsDir() && s.Nlink > 1
}

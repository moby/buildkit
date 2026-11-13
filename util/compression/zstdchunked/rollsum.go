/*
Copyright 2011 The Perkeep Authors

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

// This file is derived from go.podman.io/storage/pkg/chunked/compressor/rollsum.go
// (itself derived from Perkeep's rollsum package).

package zstdchunked

const (
	windowSize = 64 // roll assumes windowSize is a power of 2
	charOffset = 31
)

// rollSum implements a rolling checksum similar to apenwarr's bup, which is
// similar to librsync. It is used to find content-defined chunk boundaries
// inside regular files, so that unchanged chunks of a file can be reused by
// consumers across image versions.
//
// The splitting behavior must stay byte-for-byte identical to the
// containers/storage implementation so that the chunks produced here match
// the ones produced by other zstd:chunked writers for the same content.
type rollSum struct {
	s1, s2 uint32
	window [windowSize]uint8
	wofs   int
}

func newRollSum() *rollSum {
	return &rollSum{
		s1: windowSize * charOffset,
		s2: windowSize * (windowSize - 1) * charOffset,
	}
}

func (rs *rollSum) add(drop, add uint32) {
	s1 := rs.s1 + add - drop
	rs.s1 = s1
	rs.s2 += s1 - uint32(windowSize)*(drop+charOffset)
}

// roll adds ch to the rolling sum.
func (rs *rollSum) roll(ch byte) {
	wp := &rs.window[rs.wofs]
	rs.add(uint32(*wp), uint32(ch))
	*wp = ch
	rs.wofs = (rs.wofs + 1) & (windowSize - 1)
}

// onSplitWithBits reports whether at least n consecutive trailing bits
// of the current checksum are set the same way.
func (rs *rollSum) onSplitWithBits(n uint32) bool {
	mask := (uint32(1) << n) - 1
	return rs.s2&mask == (^uint32(0))&mask
}

// digest returns the current value of the rolling checksum.
func (rs *rollSum) digest() uint32 {
	return (rs.s1 << 16) | (rs.s2 & 0xffff)
}

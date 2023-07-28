//go:build amd64 && !appengine && !noasm && gc
// +build amd64,!appengine,!noasm,gc

// This file contains the specialisation of Decoder.Decompress4X
<<<<<<< HEAD
// and Decoder.Decompress1X that use an asm implementation of thir main loops.
=======
// that uses an asm implementation of its main loop.
>>>>>>> origin/v0.10
package huff0

import (
	"errors"
	"fmt"
<<<<<<< HEAD

	"github.com/klauspost/compress/internal/cpuinfo"
=======
>>>>>>> origin/v0.10
)

// decompress4x_main_loop_x86 is an x86 assembler implementation
// of Decompress4X when tablelog > 8.
<<<<<<< HEAD
//
//go:noescape
func decompress4x_main_loop_amd64(ctx *decompress4xContext)
=======
// go:noescape
func decompress4x_main_loop_x86(pbr0, pbr1, pbr2, pbr3 *bitReaderShifted,
	peekBits uint8, buf *byte, tbl *dEntrySingle) uint8
>>>>>>> origin/v0.10

// decompress4x_8b_loop_x86 is an x86 assembler implementation
// of Decompress4X when tablelog <= 8 which decodes 4 entries
// per loop.
<<<<<<< HEAD
//
//go:noescape
func decompress4x_8b_main_loop_amd64(ctx *decompress4xContext)
=======
// go:noescape
func decompress4x_8b_loop_x86(pbr0, pbr1, pbr2, pbr3 *bitReaderShifted,
	peekBits uint8, buf *byte, tbl *dEntrySingle) uint8
>>>>>>> origin/v0.10

// fallback8BitSize is the size where using Go version is faster.
const fallback8BitSize = 800

<<<<<<< HEAD
type decompress4xContext struct {
	pbr      *[4]bitReaderShifted
	peekBits uint8
	out      *byte
	dstEvery int
	tbl      *dEntrySingle
	decoded  int
	limit    *byte
}

=======
>>>>>>> origin/v0.10
// Decompress4X will decompress a 4X encoded stream.
// The length of the supplied input must match the end of a block exactly.
// The *capacity* of the dst slice must match the destination size of
// the uncompressed data exactly.
func (d *Decoder) Decompress4X(dst, src []byte) ([]byte, error) {
	if len(d.dt.single) == 0 {
		return nil, errors.New("no table loaded")
	}
	if len(src) < 6+(4*1) {
		return nil, errors.New("input too small")
	}

	use8BitTables := d.actualTableLog <= 8
	if cap(dst) < fallback8BitSize && use8BitTables {
		return d.decompress4X8bit(dst, src)
	}
<<<<<<< HEAD

=======
>>>>>>> origin/v0.10
	var br [4]bitReaderShifted
	// Decode "jump table"
	start := 6
	for i := 0; i < 3; i++ {
		length := int(src[i*2]) | (int(src[i*2+1]) << 8)
		if start+length >= len(src) {
			return nil, errors.New("truncated input (or invalid offset)")
		}
		err := br[i].init(src[start : start+length])
		if err != nil {
			return nil, err
		}
		start += length
	}
	err := br[3].init(src[start:])
	if err != nil {
		return nil, err
	}

	// destination, offset to match first output
	dstSize := cap(dst)
	dst = dst[:dstSize]
	out := dst
	dstEvery := (dstSize + 3) / 4

	const tlSize = 1 << tableLogMax
	const tlMask = tlSize - 1
	single := d.dt.single[:tlSize]

<<<<<<< HEAD
	var decoded int

	if len(out) > 4*4 && !(br[0].off < 4 || br[1].off < 4 || br[2].off < 4 || br[3].off < 4) {
		ctx := decompress4xContext{
			pbr:      &br,
			peekBits: uint8((64 - d.actualTableLog) & 63), // see: bitReaderShifted.peekBitsFast()
			out:      &out[0],
			dstEvery: dstEvery,
			tbl:      &single[0],
			limit:    &out[dstEvery-4], // Always stop decoding when first buffer gets here to avoid writing OOB on last.
		}
		if use8BitTables {
			decompress4x_8b_main_loop_amd64(&ctx)
		} else {
			decompress4x_main_loop_amd64(&ctx)
		}

		decoded = ctx.decoded
		out = out[decoded/4:]
=======
	// Use temp table to avoid bound checks/append penalty.
	buf := d.buffer()
	var off uint8
	var decoded int

	const debug = false

	// see: bitReaderShifted.peekBitsFast()
	peekBits := uint8((64 - d.actualTableLog) & 63)

	// Decode 2 values from each decoder/loop.
	const bufoff = 256
	for {
		if br[0].off < 4 || br[1].off < 4 || br[2].off < 4 || br[3].off < 4 {
			break
		}

		if use8BitTables {
			off = decompress4x_8b_loop_x86(&br[0], &br[1], &br[2], &br[3], peekBits, &buf[0][0], &single[0])
		} else {
			off = decompress4x_main_loop_x86(&br[0], &br[1], &br[2], &br[3], peekBits, &buf[0][0], &single[0])
		}
		if debug {
			fmt.Print("DEBUG: ")
			fmt.Printf("off=%d,", off)
			for i := 0; i < 4; i++ {
				fmt.Printf(" br[%d]={bitsRead=%d, value=%x, off=%d}",
					i, br[i].bitsRead, br[i].value, br[i].off)
			}
			fmt.Println("")
		}

		if off != 0 {
			break
		}

		if bufoff > dstEvery {
			d.bufs.Put(buf)
			return nil, errors.New("corruption detected: stream overrun 1")
		}
		copy(out, buf[0][:])
		copy(out[dstEvery:], buf[1][:])
		copy(out[dstEvery*2:], buf[2][:])
		copy(out[dstEvery*3:], buf[3][:])
		out = out[bufoff:]
		decoded += bufoff * 4
		// There must at least be 3 buffers left.
		if len(out) < dstEvery*3 {
			d.bufs.Put(buf)
			return nil, errors.New("corruption detected: stream overrun 2")
		}
	}
	if off > 0 {
		ioff := int(off)
		if len(out) < dstEvery*3+ioff {
			d.bufs.Put(buf)
			return nil, errors.New("corruption detected: stream overrun 3")
		}
		copy(out, buf[0][:off])
		copy(out[dstEvery:], buf[1][:off])
		copy(out[dstEvery*2:], buf[2][:off])
		copy(out[dstEvery*3:], buf[3][:off])
		decoded += int(off) * 4
		out = out[off:]
>>>>>>> origin/v0.10
	}

	// Decode remaining.
	remainBytes := dstEvery - (decoded / 4)
	for i := range br {
		offset := dstEvery * i
		endsAt := offset + remainBytes
		if endsAt > len(out) {
			endsAt = len(out)
		}
		br := &br[i]
		bitsLeft := br.remaining()
		for bitsLeft > 0 {
			br.fill()
			if offset >= endsAt {
<<<<<<< HEAD
=======
				d.bufs.Put(buf)
>>>>>>> origin/v0.10
				return nil, errors.New("corruption detected: stream overrun 4")
			}

			// Read value and increment offset.
			val := br.peekBitsFast(d.actualTableLog)
			v := single[val&tlMask].entry
			nBits := uint8(v)
			br.advance(nBits)
			bitsLeft -= uint(nBits)
			out[offset] = uint8(v >> 8)
			offset++
		}
		if offset != endsAt {
<<<<<<< HEAD
=======
			d.bufs.Put(buf)
>>>>>>> origin/v0.10
			return nil, fmt.Errorf("corruption detected: short output block %d, end %d != %d", i, offset, endsAt)
		}
		decoded += offset - dstEvery*i
		err = br.close()
		if err != nil {
			return nil, err
		}
	}
<<<<<<< HEAD
=======
	d.bufs.Put(buf)
>>>>>>> origin/v0.10
	if dstSize != decoded {
		return nil, errors.New("corruption detected: short output block")
	}
	return dst, nil
}
<<<<<<< HEAD

// decompress4x_main_loop_x86 is an x86 assembler implementation
// of Decompress1X when tablelog > 8.
//
//go:noescape
func decompress1x_main_loop_amd64(ctx *decompress1xContext)

// decompress4x_main_loop_x86 is an x86 with BMI2 assembler implementation
// of Decompress1X when tablelog > 8.
//
//go:noescape
func decompress1x_main_loop_bmi2(ctx *decompress1xContext)

type decompress1xContext struct {
	pbr      *bitReaderShifted
	peekBits uint8
	out      *byte
	outCap   int
	tbl      *dEntrySingle
	decoded  int
}

// Error reported by asm implementations
const error_max_decoded_size_exeeded = -1

// Decompress1X will decompress a 1X encoded stream.
// The cap of the output buffer will be the maximum decompressed size.
// The length of the supplied input must match the end of a block exactly.
func (d *Decoder) Decompress1X(dst, src []byte) ([]byte, error) {
	if len(d.dt.single) == 0 {
		return nil, errors.New("no table loaded")
	}
	var br bitReaderShifted
	err := br.init(src)
	if err != nil {
		return dst, err
	}
	maxDecodedSize := cap(dst)
	dst = dst[:maxDecodedSize]

	const tlSize = 1 << tableLogMax
	const tlMask = tlSize - 1

	if maxDecodedSize >= 4 {
		ctx := decompress1xContext{
			pbr:      &br,
			out:      &dst[0],
			outCap:   maxDecodedSize,
			peekBits: uint8((64 - d.actualTableLog) & 63), // see: bitReaderShifted.peekBitsFast()
			tbl:      &d.dt.single[0],
		}

		if cpuinfo.HasBMI2() {
			decompress1x_main_loop_bmi2(&ctx)
		} else {
			decompress1x_main_loop_amd64(&ctx)
		}
		if ctx.decoded == error_max_decoded_size_exeeded {
			return nil, ErrMaxDecodedSizeExceeded
		}

		dst = dst[:ctx.decoded]
	}

	// br < 8, so uint8 is fine
	bitsLeft := uint8(br.off)*8 + 64 - br.bitsRead
	for bitsLeft > 0 {
		br.fill()
		if len(dst) >= maxDecodedSize {
			br.close()
			return nil, ErrMaxDecodedSizeExceeded
		}
		v := d.dt.single[br.peekBitsFast(d.actualTableLog)&tlMask]
		nBits := uint8(v.entry)
		br.advance(nBits)
		bitsLeft -= nBits
		dst = append(dst, uint8(v.entry>>8))
	}
	return dst, br.close()
}
=======
>>>>>>> origin/v0.10

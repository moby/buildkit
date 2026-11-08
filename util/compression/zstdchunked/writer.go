package zstdchunked

import (
	"bufio"
	"bytes"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/vbatts/tar-split/archive/tar"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
)

const (
	// rollsumBits is the number of trailing bits of the rolling checksum
	// that must match for a chunk boundary.
	rollsumBits = 16

	// holesThreshold is the minimum number of consecutive zero bytes
	// that are recorded as a chunk of zeros.
	holesThreshold = int64(1 << 10)
)

// encoder is a zstd encoder that can be reused for a new destination.
type encoder interface {
	io.WriteCloser
	Reset(dest io.Writer)
}

type encoderFunc func(dest io.Writer) (encoder, error)

// Writer compresses a tar stream written to it into a zstd:chunked blob.
//
// The layer annotations describing the blob are available from Annotations
// once Close has returned successfully.
type Writer struct {
	pw   *io.PipeWriter
	done chan struct{}

	mu          sync.Mutex
	err         error
	annotations map[string]string
	closeOnce   sync.Once
}

// NewWriter returns a Writer that writes a zstd:chunked blob to dest,
// compressing every frame with the given level.
func NewWriter(dest io.Writer, level zstd.EncoderLevel) (*Writer, error) {
	newEncoder := func(dest io.Writer) (encoder, error) {
		return zstd.NewWriter(dest, zstd.WithEncoderLevel(level))
	}
	return newWriter(dest, newEncoder), nil
}

func newWriter(dest io.Writer, newEncoder encoderFunc) *Writer {
	pr, pw := io.Pipe()
	w := &Writer{
		pw:   pw,
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		a, err := writeStream(dest, pr, newEncoder)
		// writeStream normally consumes all of pr. If it failed, make sure
		// writes to the other end of the pipe don't block forever.
		pr.CloseWithError(errors.Wrap(errOrClosed(err), "zstd:chunked writer"))
		w.mu.Lock()
		w.annotations, w.err = a, err
		w.mu.Unlock()
	}()
	return w
}

func errOrClosed(err error) error {
	if err != nil {
		return err
	}
	return io.ErrClosedPipe
}

// Write writes uncompressed tar data to the Writer.
func (w *Writer) Write(p []byte) (int, error) {
	return w.pw.Write(p)
}

// Close flushes the remaining data and writes the TOC, the tar-split data and
// the footer. It does not close the underlying writer.
func (w *Writer) Close() error {
	w.closeOnce.Do(func() {
		w.pw.Close()
	})
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Annotations returns the layer annotations describing the blob written by
// w. It returns nil if Close has not returned successfully.
func (w *Writer) Annotations() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return nil
	}
	return w.annotations
}

// countingWriter counts the bytes written to the underlying writer.
type countingWriter struct {
	w     io.Writer
	count int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.count += int64(n)
	return n, err
}

type chunk struct {
	chunkOffset int64
	offset      int64
	checksum    string
	chunkSize   int64
	chunkType   string
}

// writeStream reads a tar stream from r and writes it to destFile as a
// zstd:chunked blob.
func writeStream(destFile io.Writer, r io.Reader, newEncoder encoderFunc) (_ map[string]string, retErr error) {
	// total written so far, used to compute the offsets of the files in the
	// blob
	dest := &countingWriter{w: destFile}

	var tarSplitCompressed bytes.Buffer
	tarSplitDigester := digest.Canonical.Digester()
	tarSplitZstd, err := newEncoder(io.MultiWriter(&tarSplitCompressed, tarSplitDigester.Hash()))
	if err != nil {
		return nil, err
	}
	defer func() {
		if tarSplitZstd != nil {
			tarSplitZstd.Close()
		}
	}()
	tarSplitUncompressed := &countingWriter{w: tarSplitZstd}

	// Scope the NewInputTarStreamWithDone defer so we wait for done before
	// closing tarSplitZstd.
	entries, err := func() (_ []fileMetadata, retErr error) {
		its, done, err := asm.NewInputTarStreamWithDone(r, storage.NewJSONPacker(tarSplitUncompressed), nil)
		if err != nil {
			return nil, err
		}
		defer func() {
			its.Close()
			if doneErr := <-done; doneErr != nil && retErr == nil {
				retErr = doneErr
			}
		}()

		tr := tar.NewReader(its)
		tr.RawAccounting = true

		zw, err := newEncoder(dest)
		if err != nil {
			return nil, err
		}
		defer func() {
			if zw != nil {
				zw.Close()
			}
		}()

		// restartCompression terminates the current zstd frame and returns
		// the offset at which the next one starts.
		restartCompression := func() (int64, error) {
			if err := zw.Close(); err != nil {
				return 0, err
			}
			zw.Reset(dest)
			return dest.count, nil
		}

		buf := make([]byte, 4096)
		var entries []fileMetadata
		for {
			hdr, err := tr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}

			if _, err := zw.Write(tr.RawBytes()); err != nil {
				return nil, err
			}

			payloadDigester := digest.Canonical.Digester()
			chunkDigester := digest.Canonical.Digester()

			startOffset := int64(0)
			lastOffset := int64(0)
			lastChunkOffset := int64(0)
			checksum := ""
			var chunks []chunk

			rcReader := &rollingChecksumReader{
				reader: &holesFinder{
					threshold: holesThreshold,
					reader:    bufio.NewReader(tr),
				},
				rollsum: newRollSum(),
			}

			payloadDest := io.MultiWriter(payloadDigester.Hash(), chunkDigester.Hash(), zw)
			for {
				mustSplit, read, errRead := rcReader.Read(buf)
				if errRead != nil && !errors.Is(errRead, io.EOF) {
					return nil, errRead
				}
				// start a new frame only if there is a payload
				if read > 0 {
					if startOffset == 0 {
						startOffset, err = restartCompression()
						if err != nil {
							return nil, err
						}
						lastOffset = startOffset
					}
					if _, err := payloadDest.Write(buf[:read]); err != nil {
						return nil, err
					}
				}
				if (mustSplit || errors.Is(errRead, io.EOF)) && startOffset > 0 {
					off, err := restartCompression()
					if err != nil {
						return nil, err
					}

					chunkSize := rcReader.writtenOut - lastChunkOffset
					if chunkSize > 0 {
						chunkType := chunkTypeData
						if rcReader.isLastChunkZeros {
							chunkType = chunkTypeZeros
						}
						chunks = append(chunks, chunk{
							chunkOffset: lastChunkOffset,
							offset:      lastOffset,
							checksum:    chunkDigester.Digest().String(),
							chunkSize:   chunkSize,
							chunkType:   chunkType,
						})
					}

					lastOffset = off
					lastChunkOffset = rcReader.writtenOut
					chunkDigester = digest.Canonical.Digester()
					payloadDest = io.MultiWriter(payloadDigester.Hash(), chunkDigester.Hash(), zw)
				}
				if errors.Is(errRead, io.EOF) {
					if startOffset > 0 {
						checksum = payloadDigester.Digest().String()
					}
					break
				}
			}

			mainEntry, err := newFileMetadata(hdr)
			if err != nil {
				return nil, err
			}
			mainEntry.Digest = checksum
			mainEntry.Offset = startOffset
			mainEntry.EndOffset = lastOffset
			fileEntries := []fileMetadata{mainEntry}
			for i := 1; i < len(chunks); i++ {
				fileEntries = append(fileEntries, fileMetadata{
					Type:        typeChunk,
					Name:        hdr.Name,
					ChunkOffset: chunks[i].chunkOffset,
				})
			}
			if len(chunks) > 1 {
				for i := range chunks {
					fileEntries[i].ChunkSize = chunks[i].chunkSize
					fileEntries[i].Offset = chunks[i].offset
					fileEntries[i].ChunkDigest = chunks[i].checksum
					fileEntries[i].ChunkType = chunks[i].chunkType
				}
			}
			entries = append(entries, fileEntries...)
		}

		if _, err := zw.Write(tr.RawBytes()); err != nil {
			return nil, err
		}

		// Make sure the entire tarball is flushed to the output as it might
		// contain trailing zeros that affect the uncompressed digest.
		if _, err := io.Copy(zw, its); err != nil {
			return nil, err
		}

		if err := zw.Close(); err != nil {
			return nil, err
		}
		zw = nil
		return entries, nil
	}()
	if err != nil {
		return nil, err
	}

	if err := tarSplitZstd.Close(); err != nil {
		return nil, err
	}
	tarSplitZstd = nil

	ts := &tarSplitData{
		data:             tarSplitCompressed.Bytes(),
		digest:           tarSplitDigester.Digest(),
		uncompressedSize: tarSplitUncompressed.count,
	}
	return writeManifest(dest, uint64(dest.count), ts, entries, newEncoder)
}

const (
	holesFinderStateRead = iota
	holesFinderStateAccumulate
	holesFinderStateFound
	holesFinderStateEOF
)

// holesFinder reads a stream byte by byte and reports runs of at least
// threshold consecutive zero bytes.
type holesFinder struct {
	reader    *bufio.Reader
	zeros     int64
	threshold int64

	state int
}

// readByte reads a single byte from the underlying reader.
// If a single byte is read, the return value is (0, RAW-BYTE-VALUE, nil).
// If there are at least f.threshold consecutive zeros, then the
// return value is (N_CONSECUTIVE_ZEROS, '\x00').
func (f *holesFinder) readByte() (int64, byte, error) {
	for {
		switch f.state {
		// reading the file stream
		case holesFinderStateRead:
			if f.zeros > 0 {
				f.zeros--
				return 0, 0, nil
			}
			b, err := f.reader.ReadByte()
			if err != nil {
				return 0, b, err
			}

			if b != 0 {
				return 0, b, err
			}

			f.zeros = 1
			if f.zeros == f.threshold {
				f.state = holesFinderStateFound
			} else {
				f.state = holesFinderStateAccumulate
			}
		// accumulating zeros, but still didn't reach the threshold
		case holesFinderStateAccumulate:
			b, err := f.reader.ReadByte()
			if err != nil {
				if errors.Is(err, io.EOF) {
					f.state = holesFinderStateEOF
					continue
				}
				return 0, b, err
			}

			if b == 0 {
				f.zeros++
				if f.zeros == f.threshold {
					f.state = holesFinderStateFound
				}
			} else {
				if err := f.reader.UnreadByte(); err != nil {
					return 0, 0, err
				}
				f.state = holesFinderStateRead
			}
		// found a hole. Number of zeros >= threshold
		case holesFinderStateFound:
			b, err := f.reader.ReadByte()
			if err != nil {
				if errors.Is(err, io.EOF) {
					f.state = holesFinderStateEOF
				}
				holeLen := f.zeros
				f.zeros = 0
				return holeLen, 0, nil
			}
			if b != 0 {
				if err := f.reader.UnreadByte(); err != nil {
					return 0, 0, err
				}
				f.state = holesFinderStateRead

				holeLen := f.zeros
				f.zeros = 0
				return holeLen, 0, nil
			}
			f.zeros++
		// reached EOF. Flush pending zeros if any.
		case holesFinderStateEOF:
			if f.zeros > 0 {
				f.zeros--
				return 0, 0, nil
			}
			return 0, 0, io.EOF
		}
	}
}

// rollingChecksumReader splits a file payload into chunks, either at
// boundaries found by the rolling checksum or around holes.
type rollingChecksumReader struct {
	reader      *holesFinder
	closed      bool
	rollsum     *rollSum
	pendingHole int64

	// writtenOut is the total number of bytes read from the stream.
	writtenOut int64

	// isLastChunkZeros tells whether the last generated chunk is a hole
	// (made of consecutive zeros). If it is false, then the last chunk is
	// a data chunk generated by the rolling checksum.
	isLastChunkZeros bool
}

// Read reads up to len(b) bytes into b. It returns true if a chunk boundary
// was found after the returned bytes.
func (rc *rollingChecksumReader) Read(b []byte) (bool, int, error) {
	rc.isLastChunkZeros = false

	if rc.pendingHole > 0 {
		toCopy := min(rc.pendingHole, int64(len(b)))
		rc.pendingHole -= toCopy
		clear(b[:toCopy])

		rc.writtenOut += toCopy

		rc.isLastChunkZeros = true

		// if there are no other zeros left, terminate the chunk
		return rc.pendingHole == 0, int(toCopy), nil
	}

	if rc.closed {
		return false, 0, io.EOF
	}

	for i := range b {
		holeLen, n, err := rc.reader.readByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				rc.closed = true
				if i == 0 {
					return false, 0, err
				}
				return false, i, nil
			}
			// report any other error type
			return false, -1, err
		}
		if holeLen > 0 {
			for range holeLen {
				rc.rollsum.roll(0)
			}
			rc.pendingHole = holeLen
			return true, i, nil
		}
		b[i] = n
		rc.writtenOut++
		rc.rollsum.roll(n)
		if rc.rollsum.onSplitWithBits(rollsumBits) {
			return true, i + 1, nil
		}
	}
	return false, len(b), nil
}

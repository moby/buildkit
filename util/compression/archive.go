package compression

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"encoding/binary"
	"io"
	"os/exec"

	cdcompression "github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/pkg/errors"
)

const (
	zstdSkippableMagicStart = 0x184D2A50
	zstdSkippableMagicMask  = 0xFFFFFFF0
)

var (
	// bzip2 streams start with ASCII "BZh".
	// See https://www.loc.gov/preservation/digital/formats/fdd/fdd000600.shtml.
	bzip2Magic = []byte{0x42, 0x5a, 0x68}

	// gzip streams start with ID1, ID2, and compression method bytes.
	// See https://datatracker.ietf.org/doc/html/rfc1952#section-2.3.1.
	gzipMagic = []byte{0x1f, 0x8b, 0x08}

	// XZ streams start with these six header magic bytes.
	// See https://tukaani.org/xz/xz-file-format.txt.
	xzMagic = []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}

	// Zstandard frames start with magic 0xFD2FB528, encoded little-endian.
	// See https://datatracker.ietf.org/doc/html/rfc8878#section-3.1.1.
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

// DecompressStream decompresses Docker-compatible archive streams.
func DecompressStream(r io.Reader) (io.ReadCloser, error) {
	buf := bufio.NewReaderSize(r, 32*1024)
	bs, err := buf.Peek(10)
	if err != nil && err != io.EOF {
		return nil, err
	}

	switch {
	case hasBzip2Prefix(bs):
		return readCloser{Reader: bzip2.NewReader(buf)}, nil
	case hasXZPrefix(bs):
		ctx, cancel := context.WithCancelCause(context.Background())
		xzReader, err := cmdStream(exec.CommandContext(ctx, "xz", "-d", "-c", "-q"), buf)
		if err != nil {
			cancel(err)
			return nil, err
		}
		return readCloser{
			Reader: xzReader,
			close: func() error {
				cancel(nil)
				return xzReader.Close()
			},
		}, nil
	default:
		return cdcompression.DecompressStream(buf)
	}
}

// IsArchive reports whether header looks like a supported compressed archive or tar archive.
func IsArchive(header []byte) bool {
	if hasBzip2Prefix(header) || hasGzipPrefix(header) || hasXZPrefix(header) || hasZstdPrefix(header) {
		return true
	}
	r := tar.NewReader(bytes.NewReader(header))
	_, err := r.Next()
	return err == nil
}

func hasBzip2Prefix(header []byte) bool {
	return bytes.HasPrefix(header, bzip2Magic)
}

func hasGzipPrefix(header []byte) bool {
	return bytes.HasPrefix(header, gzipMagic)
}

func hasXZPrefix(header []byte) bool {
	return bytes.HasPrefix(header, xzMagic)
}

func hasZstdPrefix(header []byte) bool {
	if bytes.HasPrefix(header, zstdMagic) {
		return true
	}
	// RFC 8878 section 3.1.2 defines skippable frame magic as 0x184D2A50 through 0x184D2A5F.
	// See https://datatracker.ietf.org/doc/html/rfc8878#section-3.1.2.
	return len(header) >= 8 && binary.LittleEndian.Uint32(header[:4])&zstdSkippableMagicMask == zstdSkippableMagicStart
}

func cmdStream(cmd *exec.Cmd, in io.Reader) (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	cmd.Stdin = in
	cmd.Stdout = writer

	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			if errBuf.Len() > 0 {
				err = errors.Wrapf(err, "%s", errBuf.String())
			}
			writer.CloseWithError(err)
		} else {
			writer.Close()
		}
	}()

	return readCloser{
		Reader: reader,
		close: func() error {
			err := reader.Close()
			<-done
			return err
		},
	}, nil
}

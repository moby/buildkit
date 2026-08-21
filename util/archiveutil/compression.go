package archiveutil

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"io"
	"os/exec"

	cdcompression "github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/pkg/errors"
)

var (
	// bzip2 streams start with ASCII "BZh".
	// See https://www.loc.gov/preservation/digital/formats/fdd/fdd000600.shtml
	bzip2Magic = []byte{0x42, 0x5a, 0x68}

	// XZ streams start with these six header magic bytes.
	// See https://tukaani.org/xz/xz-file-format.txt
	xzMagic = []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}
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
	case bytes.HasPrefix(bs, bzip2Magic):
		return readCloser{Reader: bzip2.NewReader(buf)}, nil
	case bytes.HasPrefix(bs, xzMagic):
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

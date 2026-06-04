package compression

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func (c gzipType) Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer) {
	return func(dest io.Writer, _ string) (io.WriteCloser, error) {
		switch comp.Variant {
		case "igzip":
			return gzipCmdWriter(ctx, "igzip", comp)(dest)
		case "pigz":
			return gzipCmdWriter(ctx, "pigz", comp)(dest)
		}
		return gzipWriter(comp)(dest)
	}, nil
}

func (c gzipType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return decompress(ctx, cs, desc)
}

func (c gzipType) NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error) {
	esgz, err := EStargz.Is(ctx, cs, desc.Digest)
	if err != nil {
		return false, err
	}
	if !images.IsLayerType(desc.MediaType) {
		return false, nil
	}
	ct, err := FromMediaType(desc.MediaType)
	if err != nil {
		return false, err
	}
	if ct == Gzip && !esgz {
		return false, nil
	}
	return true, nil
}

func (c gzipType) NeedsComputeDiffBySelf(comp Config) bool {
	// we allow compressing it with a customized compression level that containerd differ doesn't support so we compress it by self.
	return comp.Level != nil
}

func (c gzipType) OnlySupportOCITypes() bool {
	return false
}

func (c gzipType) MediaType() string {
	return ocispecs.MediaTypeImageLayerGzip
}

func (c gzipType) String() string {
	return "gzip"
}

func gzipWriter(comp Config) func(io.Writer) (io.WriteCloser, error) {
	return func(dest io.Writer) (io.WriteCloser, error) {
		level := gzip.DefaultCompression
		if comp.Level != nil {
			level = *comp.Level
		}
		return gzip.NewWriterLevel(dest, level)
	}
}

type writeCloserWrapper struct {
	io.Writer
	closer func() error
}

func (w *writeCloserWrapper) Close() error {
	return w.closer()
}

func gzipCmdWriter(ctx context.Context, cmd string, comp Config) func(io.Writer) (io.WriteCloser, error) {
	return func(dest io.Writer) (io.WriteCloser, error) {
		reader, writer := io.Pipe()
		args := []string{"-c"}
		if comp.Level != nil {
			args = append(args, fmt.Sprintf("-%d", *comp.Level))
		}
		command := exec.CommandContext(ctx, cmd, args...)
		command.Stdin = reader
		command.Stdout = dest

		var errBuf bytes.Buffer
		command.Stderr = &errBuf

		if err := command.Start(); err != nil {
			return nil, err
		}

		return &writeCloserWrapper{
			Writer: writer,
			closer: func() error {
				closeErr := writer.Close()
				waitErr := command.Wait()
				if waitErr != nil {
					return fmt.Errorf("%s: %s", waitErr, errBuf.String())
				}
				return errors.Join(closeErr, waitErr)
			},
		}, nil
	}
}

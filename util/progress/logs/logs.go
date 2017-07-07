package logs

import (
	"context"
	"io"
	"os"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
)

func NewLogStreams(ctx context.Context) (io.WriteCloser, io.WriteCloser) {
	return newStreamWriter(ctx, 1), newStreamWriter(ctx, 2)
}

func newStreamWriter(ctx context.Context, stream int) io.WriteCloser {
	pw, _, _ := progress.FromContext(ctx)
	return &streamWriter{
		pw:     pw,
		stream: stream,
	}
}

type streamWriter struct {
	pw     progress.Writer
	stream int
}

func (sw *streamWriter) Write(dt []byte) (int, error) {
	sw.pw.Write(identity.NewID(), client.VertexLog{
		Stream: sw.stream,
		Data:   append([]byte{}, dt...),
	})
	// TODO: remove debug
	switch sw.stream {
	case 1:
		return os.Stdout.Write(dt)
	case 2:
		return os.Stderr.Write(dt)
	default:
		return 0, errors.Errorf("invalid stream %d", sw.stream)
	}
}

func (sw *streamWriter) Close() error {
	return sw.pw.Close()
}

package contentutil

import (
	"bytes"
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCopy(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b0 := NewBuffer()
	b1 := NewBuffer()

	err := content.WriteBlob(ctx, b0, "foo", bytes.NewBuffer([]byte("foobar")), ocispecs.Descriptor{Size: -1})
	require.NoError(t, err)

	err = Copy(ctx, b1, b0, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar")), Size: -1}, "", nil)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, b1, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt))
}

func TestWriteBlob(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b := NewBuffer()
	dt := []byte("foobar")
	desc := blobDesc(dt)

	require.NoError(t, WriteBlob(ctx, b, dt, desc, "", nil))

	out, err := content.ReadBlob(ctx, b, desc)
	require.NoError(t, err)
	require.Equal(t, dt, out)
}

// TestWriteBlobRetriesTransientCommit verifies a connection dropped on the final
// commit is retried and the blob is written intact.
func TestWriteBlobRetriesTransientCommit(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b := NewBuffer()
	dt := []byte("foobar")
	desc := blobDesc(dt)
	ing := &flakyIngester{Ingester: b, failures: 2, err: connTimedOut()}

	require.NoError(t, WriteBlob(ctx, ing, dt, desc, "", nil))
	require.Equal(t, 3, ing.attempts)

	out, err := content.ReadBlob(ctx, b, desc)
	require.NoError(t, err)
	require.Equal(t, dt, out)
}

// TestWriteBlobRetriesAfterPartialWrite verifies dt is re-read from the start
// after a partial write, so a retry does not commit truncated content.
func TestWriteBlobRetriesAfterPartialWrite(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b := NewBuffer()
	dt := []byte("foobar")
	desc := blobDesc(dt)
	ing := &flakyIngester{Ingester: b, failures: 1, failMidWrite: true, err: syscall.ECONNRESET}

	require.NoError(t, WriteBlob(ctx, ing, dt, desc, "", nil))
	require.Equal(t, 2, ing.attempts)

	out, err := content.ReadBlob(ctx, b, desc)
	require.NoError(t, err)
	require.Equal(t, dt, out)
}

func TestWriteBlobDoesNotRetryPermanentError(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b := NewBuffer()
	dt := []byte("foobar")
	ing := &flakyIngester{Ingester: b, failures: 1, err: errors.New("unauthorized")}

	err := WriteBlob(ctx, ing, dt, blobDesc(dt), "", nil)
	require.ErrorContains(t, err, "unauthorized")
	require.Equal(t, 1, ing.attempts)
}

func blobDesc(dt []byte) ocispecs.Descriptor {
	return ocispecs.Descriptor{Digest: digest.FromBytes(dt), Size: int64(len(dt))}
}

func connTimedOut() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ETIMEDOUT)}
}

// flakyIngester fails the first failures writes with err, then delegates.
type flakyIngester struct {
	content.Ingester
	failures     int
	failMidWrite bool
	err          error
	attempts     int
}

func (f *flakyIngester) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	f.attempts++
	w, err := f.Ingester.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if f.failures == 0 {
		return w, nil
	}
	f.failures--
	return &flakyWriter{Writer: w, err: f.err, failMidWrite: f.failMidWrite}, nil
}

type flakyWriter struct {
	content.Writer
	err          error
	failMidWrite bool
}

func (w *flakyWriter) Write(p []byte) (int, error) {
	if !w.failMidWrite {
		return w.Writer.Write(p)
	}
	if len(p) > 1 {
		p = p[:1]
	}
	n, err := w.Writer.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.err
}

func (w *flakyWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if w.failMidWrite {
		return w.Writer.Commit(ctx, size, expected, opts...)
	}
	return w.err
}

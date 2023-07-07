package contentutil

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/containerd/containerd/content"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestFetcher(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	b0 := NewBuffer()

	err := content.WriteBlob(ctx, b0, "foo", bytes.NewBuffer([]byte("foobar")), ocispecs.Descriptor{Size: -1})
	require.NoError(t, err)

	f := &localFetcher{b0}
	p := FromFetcher(f)

	b1 := NewBuffer()
	err = Copy(ctx, b1, p, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar")), Size: -1}, "", nil)
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, b1, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)
	require.Equal(t, string(dt), "foobar")

	rdr, err := p.ReaderAt(ctx, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)

	buf := make([]byte, 3)

	n, err := rdr.ReadAt(buf, 1)
	require.NoError(t, err)
	require.Equal(t, "oob", string(buf[:n]))

	n, err = rdr.ReadAt(buf, 5)
	require.Error(t, err)
	require.Equal(t, err, io.EOF)
	require.Equal(t, "r", string(buf[:n]))
}

func TestSlowFetch(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	f := &dummySlowFetcher{}
	p := FromFetcher(f)

	rdr, err := p.ReaderAt(ctx, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)

	buf := make([]byte, 3)

	n, err := rdr.ReadAt(buf, 1)
	require.NoError(t, err)
	require.Equal(t, "oob", string(buf[:n]))

	n, err = rdr.ReadAt(buf, 5)
	require.Error(t, err)
	require.Equal(t, err, io.EOF)
	require.Equal(t, "r", string(buf[:n]))
}

type dummySlowFetcher struct{}

func (f *dummySlowFetcher) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return newSlowBuffer([]byte("foobar")), nil
}

func newSlowBuffer(dt []byte) io.ReadCloser {
	return &slowBuffer{dt: dt}
}

type slowBuffer struct {
	dt  []byte
	off int
}

func (sb *slowBuffer) Seek(offset int64, _ int) (int64, error) {
	sb.off = int(offset)
	return offset, nil
}

func (sb *slowBuffer) Read(b []byte) (int, error) {
	time.Sleep(5 * time.Millisecond)
	if sb.off >= len(sb.dt) {
		return 0, io.EOF
	}
	b[0] = sb.dt[sb.off]
	sb.off++
	return 1, nil
}

func (sb *slowBuffer) Close() error {
	return nil
}

func TestFetchNoSeek(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	f := &dummyFetcherNoSeek{
		content: []byte("foobar"),
	}
	p := FromFetcher(f)

	rdr, err := p.ReaderAt(ctx, ocispecs.Descriptor{Digest: digest.FromBytes([]byte("foobar"))})
	require.NoError(t, err)

	buf := make([]byte, 2)

	n, err := rdr.ReadAt(buf, 2)
	require.NoError(t, err)
	require.Equal(t, "ob", string(buf[:n]))

	n, err = rdr.ReadAt(buf, 5)
	require.Error(t, err)
	require.Equal(t, err, io.EOF)
	require.Equal(t, "r", string(buf[:n]))

	n, err = rdr.ReadAt(buf, 1)
	require.NoError(t, err)
	require.Equal(t, "oo", string(buf[:n]))
}

func TestFetchNoSeekBigBlock(t *testing.T) {
	t.Parallel()
	ctx := context.TODO()

	const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	s := make([]byte, 3000)
	for i := range s {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(letterBytes))))
		require.NoError(t, err)
		s[i] = letterBytes[x.Int64()]
	}

	f := &dummyFetcherNoSeek{
		content: s,
	}
	p := FromFetcher(f)

	rdr, err := p.ReaderAt(ctx, ocispecs.Descriptor{Digest: digest.FromBytes(s)})
	require.NoError(t, err)

	buf := make([]byte, 4)

	n, err := rdr.ReadAt(buf, 2)
	require.NoError(t, err)
	require.Equal(t, string(s[2:6]), string(buf[:n]))

	n, err = rdr.ReadAt(buf, 2090)
	require.NoError(t, err)
	require.Equal(t, string(s[2090:2094]), string(buf[:n]))

	n, err = rdr.ReadAt(buf, 1040)
	require.NoError(t, err)
	require.Equal(t, string(s[1040:1044]), string(buf[:n]))
}

type dummyFetcherNoSeek struct {
	content []byte
}

func (f *dummyFetcherNoSeek) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return newBufferNoSeek(f.content), nil
}

func newBufferNoSeek(dt []byte) io.ReadCloser {
	return &bufferNoSeek{dt: dt}
}

type bufferNoSeek struct {
	dt  []byte
	off int
}

func (bns *bufferNoSeek) Read(b []byte) (int, error) {
	if bns.off >= len(bns.dt) {
		return 0, io.EOF
	}
	b[0] = bns.dt[bns.off]
	bns.off++
	return 1, nil
}

func (bns *bufferNoSeek) Close() error {
	return nil
}

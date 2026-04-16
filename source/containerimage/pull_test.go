package containerimage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

const testPushRef = "push.example.com/test:latest"

func testDesc(payload []byte) ocispecs.Descriptor {
	return ocispecs.Descriptor{
		Digest: digest.FromBytes(payload),
		Size:   int64(len(payload)),
	}
}

// fakeResolver implements just enough of remotes.Resolver to back
// pushFallbackProvider in tests. Only Fetcher is exercised.
type fakeResolver struct {
	fetcherFn func(ctx context.Context, ref string) (remotes.Fetcher, error)
}

func (r *fakeResolver) Resolve(ctx context.Context, ref string) (string, ocispecs.Descriptor, error) {
	return ref, ocispecs.Descriptor{}, errors.New("not implemented")
}
func (r *fakeResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	return r.fetcherFn(ctx, ref)
}
func (r *fakeResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	return nil, errors.New("not implemented")
}

// fetcherFunc adapts a function to remotes.Fetcher for terse test setup.
type fetcherFunc func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error)

func (f fetcherFunc) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return f(ctx, desc)
}

// originProvider serves a fixed payload and counts ReaderAt invocations so
// tests can assert whether the fallback was exercised.
type originProvider struct {
	payload []byte
	calls   int32
}

func (o *originProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	atomic.AddInt32(&o.calls, 1)
	return &bytesReaderAt{data: o.payload}, nil
}

type bytesReaderAt struct {
	data []byte
}

func (b *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
func (b *bytesReaderAt) Close() error { return nil }
func (b *bytesReaderAt) Size() int64  { return int64(len(b.data)) }

// seekableBuffer wraps bytes.Reader as an io.ReadCloser+Seeker so it works
// with the FromFetcher readerAt wrapper which seeks for offset reads.
type seekableBuffer struct {
	*bytes.Reader
}

func (s *seekableBuffer) Close() error { return nil }

func newSeekableBuffer(data []byte) io.ReadCloser {
	return &seekableBuffer{Reader: bytes.NewReader(data)}
}

// blockingReader blocks Read until ctx is done, simulating a slow registry.
type blockingReader struct {
	ctx context.Context
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *blockingReader) Close() error { return nil }

func TestPushFallbackProvider_Hit(t *testing.T) {
	t.Parallel()

	pushPayload := []byte("from-push-registry")
	originPayload := []byte("from-origin")

	var fetcherCalls int32
	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return fetcherFunc(func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
				atomic.AddInt32(&fetcherCalls, 1)
				return newSeekableBuffer(pushPayload), nil
			}), nil
		},
	}
	origin := &originProvider{payload: originPayload}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	ra, err := p.ReaderAt(context.Background(), testDesc(pushPayload))
	require.NoError(t, err)
	defer ra.Close()

	require.Equal(t, int32(0), atomic.LoadInt32(&origin.calls), "origin should not be called on hit")
	// Probe + real read both call Fetcher exactly once.
	require.Equal(t, int32(2), atomic.LoadInt32(&fetcherCalls))

	got := make([]byte, len(pushPayload))
	n, err := ra.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, pushPayload, got[:n])
}

func TestPushFallbackProvider_NotFound(t *testing.T) {
	t.Parallel()

	originPayload := []byte("from-origin")

	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return fetcherFunc(func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
				return nil, errors.New("blob not found")
			}), nil
		},
	}
	origin := &originProvider{payload: originPayload}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	ra, err := p.ReaderAt(context.Background(), testDesc(originPayload))
	require.NoError(t, err)
	defer ra.Close()

	require.Equal(t, int32(1), atomic.LoadInt32(&origin.calls), "origin should be called on miss")

	got := make([]byte, len(originPayload))
	n, err := ra.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, originPayload, got[:n])
}

func TestPushFallbackProvider_FetcherError(t *testing.T) {
	t.Parallel()

	originPayload := []byte("from-origin")

	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return nil, errors.New("auth failed")
		},
	}
	origin := &originProvider{payload: originPayload}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	ra, err := p.ReaderAt(context.Background(), testDesc(originPayload))
	require.NoError(t, err)
	defer ra.Close()

	require.Equal(t, int32(1), atomic.LoadInt32(&origin.calls), "origin should be called when fetcher errors")
}

func TestPushFallbackProvider_EmptyBlobIsHit(t *testing.T) {
	t.Parallel()

	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return fetcherFunc(func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
				return newSeekableBuffer(nil), nil
			}), nil
		},
	}
	origin := &originProvider{payload: []byte("origin-fallback")}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	ra, err := p.ReaderAt(context.Background(), testDesc(nil))
	require.NoError(t, err)
	defer ra.Close()

	// EOF on the 1-byte probe of an empty blob is not a "missing" signal —
	// the blob exists, it's just empty. We should commit to the push reader.
	require.Equal(t, int32(0), atomic.LoadInt32(&origin.calls))
}

func TestPushFallbackProvider_ProbeTimesOut(t *testing.T) {
	t.Parallel()

	prevTimeout := pushRegistryProbeTimeout
	pushRegistryProbeTimeout = 50 * time.Millisecond
	defer func() { pushRegistryProbeTimeout = prevTimeout }()

	originPayload := []byte("from-origin")

	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return fetcherFunc(func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
				return &blockingReader{ctx: ctx}, nil
			}), nil
		},
	}
	origin := &originProvider{payload: originPayload}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	start := time.Now()
	ra, err := p.ReaderAt(context.Background(), testDesc(originPayload))
	elapsed := time.Since(start)

	require.NoError(t, err)
	defer ra.Close()
	require.Equal(t, int32(1), atomic.LoadInt32(&origin.calls), "origin should be called when probe times out")
	require.Less(t, elapsed, 1*time.Second, "should fall back well before the parent ctx deadline")
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "should wait for the probe timeout")
}

func TestPushFallbackProvider_ReturnedReaderIsolatedFromProbeTimeout(t *testing.T) {
	t.Parallel()

	prevTimeout := pushRegistryProbeTimeout
	pushRegistryProbeTimeout = 50 * time.Millisecond
	defer func() { pushRegistryProbeTimeout = prevTimeout }()

	pushPayload := []byte("from-push-registry")

	push := &fakeResolver{
		fetcherFn: func(ctx context.Context, ref string) (remotes.Fetcher, error) {
			return fetcherFunc(func(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
				return newSeekableBuffer(pushPayload), nil
			}), nil
		},
	}
	origin := &originProvider{payload: []byte("origin-fallback")}

	p := &pushFallbackProvider{pushResolver: push, pushRef: testPushRef, origin: origin}

	ra, err := p.ReaderAt(context.Background(), testDesc(pushPayload))
	require.NoError(t, err)
	defer ra.Close()

	// Sleep past the probe timeout. If the returned ReaderAt's underlying
	// context were the (now-canceled) probeCtx, the read below would fail.
	time.Sleep(2 * pushRegistryProbeTimeout)

	got := make([]byte, len(pushPayload))
	n, err := ra.ReadAt(got, 0)
	require.NoError(t, err)
	require.Equal(t, pushPayload, got[:n])
}

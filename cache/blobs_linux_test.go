package cache

import (
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestTryComputeOverlayBlobWaitsForStableRef(t *testing.T) {
	t.Parallel()

	expected := errors.New("truncate failed")
	writer := &truncateFailureWriter{err: expected}
	store := &retryWriterStore{failures: 2, writer: writer}
	sr := &immutableRef{cacheRecord: &cacheRecord{cm: &cacheManager{ContentStore: store}}}

	_, ok, err := sr.tryComputeOverlayBlob(t.Context(), nil, []mount.Mount{{Type: "bind", Source: t.TempDir()}}, "application/test", "record", nil)
	require.ErrorIs(t, err, expected)
	require.False(t, ok)
	require.Equal(t, []string{"record", "record", "record"}, store.references())
	require.True(t, writer.closed)
	require.Zero(t, store.abortCount(), "a stable ingest reference must not be aborted after close")
}

func (s *retryWriterStore) abortCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborts
}

type truncateFailureWriter struct {
	content.Writer

	err    error
	closed bool
}

func (w *truncateFailureWriter) Truncate(int64) error {
	return w.err
}

func (w *truncateFailureWriter) Close() error {
	w.closed = true
	return nil
}

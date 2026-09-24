package cache

import (
	"context"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/mount"
	cerrdefs "github.com/containerd/errdefs"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestOpenWriterStoreRetriesUnavailable(t *testing.T) {
	t.Parallel()

	store := &retryWriterStore{failures: 2, writer: &stubWriter{}}
	w, err := (openWriterStore{Store: store}).Writer(t.Context(), content.WithRef("record"))
	require.NoError(t, err)
	require.NotNil(t, w)
	require.Equal(t, []string{"record", "record", "record"}, store.references())
}

func TestCompareWithRetry(t *testing.T) {
	t.Parallel()

	expected := ocispecs.Descriptor{MediaType: "application/test"}
	comparer := &retryComparer{failures: 2, desc: expected}
	var optsCalls int
	desc, err := compareWithRetry(t.Context(), comparer, nil, nil, func() []diff.Opt {
		optsCalls++
		return []diff.Opt{diff.WithReference("record")}
	})
	require.NoError(t, err)
	require.Equal(t, expected, desc)
	require.Equal(t, []string{"record", "record", "record"}, comparer.references())
	require.Equal(t, 3, optsCalls, "each comparison attempt must receive fresh options")
}

func TestCompareWithRetryStopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	expected := errors.New("comparison failed")
	comparer := &retryComparer{err: expected}
	_, err := compareWithRetry(t.Context(), comparer, nil, nil, func() []diff.Opt { return nil })
	require.ErrorIs(t, err, expected)
	require.Equal(t, 1, comparer.attempts())
}

func TestCompareWithRetryStopsOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("canceled"))
	comparer := &retryComparer{failures: -1}
	_, err := compareWithRetry(ctx, comparer, nil, nil, func() []diff.Opt { return nil })
	require.ErrorIs(t, err, cerrdefs.ErrUnavailable)
	require.Equal(t, 1, comparer.attempts())
}

type retryWriterStore struct {
	content.Store

	mu       sync.Mutex
	count    int
	failures int
	writer   content.Writer
	refs     []string
	aborts   int
}

func (s *retryWriterStore) Writer(_ context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var cfg content.WriterOpts
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	s.refs = append(s.refs, cfg.Ref)
	if s.failures < 0 || s.count <= s.failures {
		return nil, errors.WithStack(cerrdefs.ErrUnavailable)
	}
	return s.writer, nil
}

func (s *retryWriterStore) Abort(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborts++
	return errors.New("unexpected abort of stable ingest reference")
}

func (s *retryWriterStore) references() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refs...)
}

type stubWriter struct {
	content.Writer
}

type retryComparer struct {
	mu       sync.Mutex
	count    int
	failures int
	err      error
	desc     ocispecs.Descriptor
	refs     []string
}

func (c *retryComparer) Compare(_ context.Context, _, _ []mount.Mount, opts ...diff.Opt) (ocispecs.Descriptor, error) {
	var cfg diff.Config
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return ocispecs.Descriptor{}, errors.WithStack(err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	c.refs = append(c.refs, cfg.Reference)
	if c.err != nil {
		return ocispecs.Descriptor{}, c.err
	}
	if c.failures < 0 || c.count <= c.failures {
		return ocispecs.Descriptor{}, errors.WithStack(cerrdefs.ErrUnavailable)
	}
	return c.desc, nil
}

func (c *retryComparer) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func (c *retryComparer) references() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.refs...)
}

package filesync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/testutil"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"
)

func TestFileSyncIncludePatterns(t *testing.T) {
	ctx := t.Context()
	t.Parallel()

	tmpDir := t.TempDir()
	tmpFS, err := fsutil.NewFS(tmpDir)
	require.NoError(t, err)
	destDir := t.TempDir()

	err = os.WriteFile(filepath.Join(tmpDir, "foo"), []byte("content1"), 0600)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(tmpDir, "bar"), []byte("content2"), 0600)
	require.NoError(t, err)

	s, err := session.NewSession(ctx, "bar")
	require.NoError(t, err)

	m, err := session.NewManager()
	require.NoError(t, err)

	fs := NewFSSyncProvider(StaticDirSource{"test0": tmpFS}, nil)
	s.Allow(fs)

	dialer := session.Dialer(testutil.TestStream(testutil.Handler(m.HandleConn)))

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return s.Run(ctx, dialer)
	})

	g.Go(func() (reterr error) {
		defer func() {
			err := s.Close()
			if reterr == nil {
				reterr = err
			}
		}()

		c, err := m.Get(ctx, s.ID(), false)
		if err != nil {
			return err
		}
		if err := FSSync(ctx, c, FSSendRequestOpt{
			Name:            "test0",
			DestDir:         destDir,
			IncludePatterns: []string{"ba*"},
		}); err != nil {
			return err
		}

		if _, err := os.ReadFile(filepath.Join(destDir, "foo")); err == nil {
			return errors.New("expected error reading foo")
		}

		dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
		if err != nil {
			return err
		}
		assert.Equal(t, "content2", string(dt))
		return nil
	})

	err = g.Wait()
	require.NoError(t, err)
}

func TestFileSyncFilterOpt(t *testing.T) {
	ctx := t.Context()
	tmpDir := t.TempDir()
	for _, name := range []string{"keep.txt", "drop.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), []byte(name), 0600))
	}
	tmpFS, err := fsutil.NewFS(tmpDir)
	require.NoError(t, err)
	destDir := t.TempDir()

	s, err := session.NewSession(ctx, "filter-opt")
	require.NoError(t, err)
	m, err := session.NewManager()
	require.NoError(t, err)
	s.Allow(NewFSSyncProvider(StaticDirSource{"context": tmpFS}, func(name string, opt *fsutil.FilterOpt) error {
		if name != "context" {
			return errors.Errorf("unexpected directory %q", name)
		}
		opt.ExcludePatterns = append([]string{"*.txt"}, opt.ExcludePatterns...)
		return nil
	}))

	dialer := session.Dialer(testutil.TestStream(testutil.Handler(m.HandleConn)))
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return s.Run(ctx, dialer) })
	g.Go(func() (reterr error) {
		defer func() {
			if err := s.Close(); reterr == nil {
				reterr = err
			}
		}()
		c, err := m.Get(ctx, s.ID(), false)
		if err != nil {
			return err
		}
		return FSSync(ctx, c, FSSendRequestOpt{
			Name:            "context",
			DestDir:         destDir,
			ExcludePatterns: []string{"!keep.txt"},
		})
	})
	require.NoError(t, g.Wait())

	data, err := os.ReadFile(filepath.Join(destDir, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep.txt", string(data))
	_, err = os.Stat(filepath.Join(destDir, "drop.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLocalExporterModeDeleteRequiresDaemonSupport(t *testing.T) {
	destDir := t.TempDir()
	staleFile := filepath.Join(destDir, "stale")
	require.NoError(t, os.WriteFile(staleFile, []byte("old"), 0600))

	target := NewFSSyncTarget(WithFSSyncDirDelete(1, destDir))
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(keyExporterID, "1"))

	err := target.DiffCopy(&testFileSendStream{ctx: ctx})
	require.ErrorContains(t, err, "local exporter mode=delete requires a BuildKit daemon")

	dt, err := os.ReadFile(staleFile)
	require.NoError(t, err)
	assert.Equal(t, "old", string(dt))
}

type testFileSendStream struct {
	ctx context.Context
}

func (s *testFileSendStream) Context() context.Context {
	return s.ctx
}

func (s *testFileSendStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *testFileSendStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *testFileSendStream) SetTrailer(metadata.MD) {
}

func (s *testFileSendStream) Send(*BytesMessage) error {
	return nil
}

func (s *testFileSendStream) Recv() (*BytesMessage, error) {
	return nil, io.EOF
}

func (s *testFileSendStream) SendMsg(any) error {
	return nil
}

func (s *testFileSendStream) RecvMsg(any) error {
	return io.EOF
}

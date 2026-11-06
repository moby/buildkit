//go:build nydus

package cache

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

type nydusTestProvider struct {
	data   []byte
	err    error
	reads  int
	closes int
	got    ocispecs.Descriptor
}

func (p *nydusTestProvider) ReaderAt(_ context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	p.reads++
	p.got = desc
	if p.err != nil {
		return nil, p.err
	}
	return &nydusTestReader{Reader: bytes.NewReader(p.data), provider: p}, nil
}

type nydusTestReader struct {
	*bytes.Reader
	provider *nydusTestProvider
}

func (r *nydusTestReader) Close() error {
	r.provider.closes++
	return nil
}

func TestNydusRemoteReaderAt(t *testing.T) {
	desc := ocispecs.Descriptor{Size: 10}
	provider := &nydusTestProvider{data: []byte("0123456789")}
	reader := &nydusRemoteReaderAt{ctx: t.Context(), provider: provider, desc: desc}

	buf := make([]byte, 3)
	n, err := reader.ReadAt(buf, 4)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, "456", string(buf))
	require.Equal(t, int64(10), reader.Size())
	require.Equal(t, desc, provider.got)
	require.Equal(t, 1, provider.reads)
	require.Equal(t, 1, provider.closes)
	require.NoError(t, reader.Close())
}

func TestNydusRemoteReaderAtFailure(t *testing.T) {
	want := errors.New("remote read failed")
	provider := &nydusTestProvider{err: want}
	reader := &nydusRemoteReaderAt{ctx: t.Context(), provider: provider}
	n, err := reader.ReadAt(make([]byte, 1), 0)
	require.ErrorIs(t, err, want)
	require.Zero(t, n)
	require.Zero(t, provider.closes)
}

package http

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/snapshot/naive"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/testutil/httpserver"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestHTTPSource(t *testing.T) {
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	hs, err := newHTTPSource(tmpdir)
	require.NoError(t, err)

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	id := &source.HttpIdentifier{URL: server.URL + "/foo"}

	h, err := hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err := h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content1")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 1)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	ref, err := h.Snapshot(ctx)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil

	// repeat, should use the etag
	h, err = hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err = h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content1")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 2)
	require.Equal(t, server.Stats("/foo").CachedRequests, 1)

	ref, err = h.Snapshot(ctx)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
			ref = nil
		}
	}()

	dt, err = readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil

	resp2 := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content2"),
	}

	// update etag, downloads again
	server.SetRoute("/foo", resp2)

	h, err = hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err = h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content2")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 3)
	require.Equal(t, server.Stats("/foo").CachedRequests, 1)

	ref, err = h.Snapshot(ctx)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
			ref = nil
		}
	}()

	dt, err = readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content2"))

	ref.Release(context.TODO())
	ref = nil
}

func TestHTTPDefaultName(t *testing.T) {
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	hs, err := newHTTPSource(tmpdir)
	require.NoError(t, err)

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/": resp,
	})
	defer server.Close()

	id := &source.HttpIdentifier{URL: server.URL}

	h, err := hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err := h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content1")).String(), k)
	require.Equal(t, server.Stats("/").AllRequests, 1)
	require.Equal(t, server.Stats("/").CachedRequests, 0)

	ref, err := h.Snapshot(ctx)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "download")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content1"))

	ref.Release(context.TODO())
	ref = nil
}

func TestHTTPInvalidURL(t *testing.T) {
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	hs, err := newHTTPSource(tmpdir)
	require.NoError(t, err)

	server := httpserver.NewTestServer(map[string]httpserver.Response{})
	defer server.Close()

	id := &source.HttpIdentifier{URL: server.URL + "/foo"}

	h, err := hs.Resolve(ctx, id)
	require.NoError(t, err)

	_, err = h.CacheKey(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response")
}

func TestHTTPChecksum(t *testing.T) {
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	hs, err := newHTTPSource(tmpdir)
	require.NoError(t, err)

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content-correct"),
	}
	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	id := &source.HttpIdentifier{URL: server.URL + "/foo", Checksum: digest.FromBytes([]byte("content-different"))}

	h, err := hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err := h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content-different")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 0)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	_, err = h.Snapshot(ctx)
	require.Error(t, err)

	require.Equal(t, digest.FromBytes([]byte("content-different")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 1)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	id = &source.HttpIdentifier{URL: server.URL + "/foo", Checksum: digest.FromBytes([]byte("content-correct"))}

	h, err = hs.Resolve(ctx, id)
	require.NoError(t, err)

	k, err = h.CacheKey(ctx)
	require.NoError(t, err)

	require.Equal(t, digest.FromBytes([]byte("content-correct")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 1)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	ref, err := h.Snapshot(ctx)
	require.NoError(t, err)
	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
			ref = nil
		}
	}()

	dt, err := readFile(ctx, ref, "foo")
	require.NoError(t, err)
	require.Equal(t, dt, []byte("content-correct"))

	require.Equal(t, digest.FromBytes([]byte("content-correct")).String(), k)
	require.Equal(t, server.Stats("/foo").AllRequests, 2)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	ref.Release(context.TODO())
	ref = nil

}

func readFile(ctx context.Context, ref cache.ImmutableRef, fp string) ([]byte, error) {
	mount, err := ref.Mount(ctx, false)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer lm.Unmount()

	dt, err := ioutil.ReadFile(filepath.Join(dir, fp))
	if err != nil {
		return nil, err
	}

	return dt, nil
}

func newHTTPSource(tmpdir string) (source.Source, error) {
	snapshotter, err := naive.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	if err != nil {
		return nil, err
	}

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, err
	}

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
	})
	if err != nil {
		return nil, err
	}

	return NewSource(Opt{
		CacheAccessor: cm,
		MetadataStore: md,
	})
}

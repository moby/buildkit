package containerimage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/session"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestFrontendPruneDoesNotBlockMetadata(t *testing.T) {
	store, err := metadata.NewStore(filepath.Join(t.TempDir(), "metadata.db"))
	require.NoError(t, err)
	defer store.Close()
	cs := &blockingFrontendStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	unblock := sync.OnceFunc(func() { close(cs.release) })
	defer unblock()
	src, err := NewSource(SourceOpt{
		MetadataStore: store,
		ContentStore:  cs,
	})
	require.NoError(t, err)
	require.NoError(t, src.setFrontendDigest("example.com/frontend:latest", digest.FromString("frontend")))
	pruned := make(chan error, 1)
	go func() { pruned <- src.PruneFrontendResolutions(t.Context()) }()
	<-cs.started
	written := make(chan error, 1)
	go func() {
		written <- store.DB().Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte("unrelated"))
			return err
		})
	}()
	select {
	case err := <-written:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Error("content lookup blocked unrelated cache metadata writes")
	}
	unblock()
	require.NoError(t, <-pruned)
}

type blockingFrontendStore struct {
	content.Store
	started chan struct{}
	release chan struct{}
}

func (s *blockingFrontendStore) Info(context.Context, digest.Digest) (content.Info, error) {
	close(s.started)
	<-s.release
	return content.Info{}, cerrdefs.ErrNotFound
}

func TestFrontendResolution(t *testing.T) {
	ctx := namespaces.WithNamespace(t.Context(), "test")
	dir := t.TempDir()
	cs, err := local.NewStore(filepath.Join(dir, "content"))
	require.NoError(t, err)
	db, err := bolt.Open(filepath.Join(dir, "content.db"), 0o600, nil)
	require.NoError(t, err)
	mdb := ctdmetadata.NewDB(db, cs, nil)
	require.NoError(t, mdb.Init(ctx))
	t.Cleanup(func() { require.NoError(t, mdb.Close()) })

	blobs := map[string][]byte{}
	configs := make([]digest.Digest, 2)
	manifests := make([]digest.Digest, 2)
	for i, version := range []string{"first", "second"} {
		config, err := json.Marshal(ocispecs.Image{
			Platform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			Config:   ocispecs.ImageConfig{Labels: map[string]string{"version": version}},
			RootFS:   ocispecs.RootFS{Type: "layers"},
		})
		require.NoError(t, err)
		configs[i] = digest.FromBytes(config)
		blobs[configs[i].String()] = config
		manifest, err := json.Marshal(ocispecs.Manifest{
			Versioned: specs.Versioned{SchemaVersion: 2},
			MediaType: ocispecs.MediaTypeImageManifest,
			Config: ocispecs.Descriptor{
				MediaType: ocispecs.MediaTypeImageConfig,
				Digest:    configs[i],
				Size:      int64(len(config)),
			},
		})
		require.NoError(t, err)
		manifests[i] = digest.FromBytes(manifest)
		blobs[manifests[i].String()] = manifest
	}
	var current atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if name == "latest" {
			name = manifests[current.Load()].String()
		}
		dt, ok := blobs[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ocispecs.MediaTypeImageManifest)
		w.Header().Set("Docker-Content-Digest", name)
		_, _ = w.Write(dt)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	var store *metadata.Store
	open := func() *Source {
		t.Helper()
		if store != nil {
			require.NoError(t, store.Close())
		}
		store, err = metadata.NewStore(filepath.Join(dir, "metadata.db"))
		require.NoError(t, err)
		src, err := NewSource(SourceOpt{
			MetadataStore: store,
			ContentStore:  mdb.ContentStore(),
			RegistryHosts: func(string) ([]docker.RegistryHost, error) {
				return []docker.RegistryHost{{
					Client:       server.Client(),
					Host:         host,
					Scheme:       "http",
					Path:         "/v2",
					Capabilities: docker.HostCapabilityResolve | docker.HostCapabilityPull,
				}}, nil
			},
		})
		require.NoError(t, err)
		return src
	}
	src := open()
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	sm, err := session.NewManager()
	require.NoError(t, err)
	ref := host + "/frontend:latest"
	resolve := func(ref, mode string, frontend bool) (*sourceresolver.ResolveImageResponse, error) {
		t.Helper()
		id, err := NewImageIdentifier(ref)
		require.NoError(t, err)
		return src.ResolveImageMetadata(ctx, id, &sourceresolver.ResolveImageOpt{
			ResolveMode: mode,
			Frontend:    frontend,
		}, sm, session.NewGroup())
	}
	_, err = resolve(ref, "never", true)
	require.ErrorContains(t, err, "pulling is disabled")
	require.Zero(t, requests.Load())

	res, err := resolve(ref, "default", true)
	require.NoError(t, err)
	require.Equal(t, manifests[0], res.Digest)
	count := requests.Load()
	require.Positive(t, count)
	// Reopen the persistent store and recreate the source, as on daemon restart.
	src = open()
	res, err = resolve(ref, "never", true)
	require.NoError(t, err)
	require.Equal(t, manifests[0], res.Digest)
	require.Equal(t, count, requests.Load())

	current.Store(1)
	res, err = resolve(ref, "default", true)
	require.NoError(t, err)
	require.Equal(t, manifests[1], res.Digest)
	count = requests.Load()
	res, err = resolve(ref+"@"+manifests[0].String(), "never", true)
	require.NoError(t, err)
	require.Equal(t, manifests[0], res.Digest)
	res, err = resolve(ref, "never", true)
	require.NoError(t, err)
	require.Equal(t, manifests[1], res.Digest)
	require.Equal(t, count, requests.Load())

	_, err = resolve(ref, "never", false)
	require.ErrorContains(t, err, "pulling is disabled")
	server.Close()
	_, err = resolve(ref, "default", true)
	require.Error(t, err)
	res, err = resolve(ref, "never", true)
	require.NoError(t, err)
	require.Equal(t, manifests[1], res.Digest)

	require.NoError(t, mdb.ContentStore().Delete(ctx, configs[1]))
	require.NoError(t, cs.Delete(ctx, configs[1]))
	_, err = resolve(ref, "never", true)
	require.ErrorContains(t, err, "pulling is disabled")
	require.Equal(t, count, requests.Load())

	// An explicitly seeded local tag takes precedence over the saved resolution,
	// even when the saved image's content has been pruned.
	src.ImageStore = &neverImageStore{target: ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageManifest,
		Digest:    manifests[0],
		Size:      int64(len(blobs[manifests[0].String()])),
	}}
	res, err = resolve(ref, "never", true)
	require.NoError(t, err)
	require.Equal(t, manifests[0], res.Digest)
	require.Equal(t, count, requests.Load())

	require.NoError(t, src.PruneFrontendResolutions(ctx))
	saved, err := src.frontendDigest(ref)
	require.NoError(t, err)
	require.Equal(t, manifests[1], saved)
	storeErr := errors.New("image store unavailable")
	src.ImageStore = &neverImageStore{err: storeErr}
	_, err = resolve(ref, "never", true)
	require.ErrorIs(t, err, storeErr)
	require.Equal(t, count, requests.Load())
	require.NoError(t, src.setFrontendDigest(ref+"-also", manifests[1]))
	require.NoError(t, src.setFrontendDigest(ref+"-kept", manifests[0]))
	require.NoError(t, mdb.ContentStore().Delete(ctx, manifests[1]))
	require.NoError(t, cs.Delete(ctx, manifests[1]))
	require.NoError(t, src.PruneFrontendResolutions(ctx))
	saved, err = src.frontendDigest(ref)
	require.NoError(t, err)
	require.Empty(t, saved)
	saved, err = src.frontendDigest(ref + "-also")
	require.NoError(t, err)
	require.Empty(t, saved)
	saved, err = src.frontendDigest(ref + "-kept")
	require.NoError(t, err)
	require.Equal(t, manifests[0], saved)
}

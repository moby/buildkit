package client

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	contentlocal "github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/client/ociindex"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestResetCacheStoreConcurrentExport(t *testing.T) {
	ctx := t.Context()
	dir, cs, manifestA := setupCacheStore(ctx, t, []byte(`{"tag":"a"}`), [][]byte{[]byte("layer-a")}, "a")
	orphan := writeBlob(ctx, t, cs, []byte("orphan"))

	export := &cacheExportStore{Store: cs, lock: flock.New(filepath.Join(dir, "cache.lock"))}
	t.Cleanup(func() { require.NoError(t, export.Close()) })
	// Merely setting up an export must not prevent cleanup during the build.
	require.NoError(t, resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock"))))
	require.NotContains(t, listDigests(ctx, t, cs), orphan)

	// Reusing an existing, unindexed blob must protect it too, even though
	// the underlying Writer returns AlreadyExists without writing anything.
	layerData := []byte("layer-b")
	layerB := writeBlob(ctx, t, cs, layerData)
	writeBlob(ctx, t, export, layerData)
	require.ErrorContains(t, resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock"))), "skipping cleanup")
	require.Contains(t, listDigests(ctx, t, cs), layerB)

	configData := []byte(`{"tag":"b"}`)
	configB := writeBlob(ctx, t, export, configData)
	manifest := ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config: ocispecs.Descriptor{
			Digest: configB, Size: int64(len(configData)), MediaType: "application/vnd.buildkit.cacheconfig.v0",
		},
		Layers: []ocispecs.Descriptor{{Digest: layerB, Size: int64(len(layerData))}},
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestB := writeBlob(ctx, t, export, manifestData)
	orphan = writeBlob(ctx, t, cs, []byte("another-orphan"))

	// All writers have closed, but the export has not published its tag yet.
	require.ErrorContains(t, resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock"))), "skipping cleanup")
	require.Len(t, listDigests(ctx, t, cs), 7)
	idx := ociindex.NewStoreIndex(dir)
	require.NoError(t, idx.Put(ocispecs.Descriptor{
		Digest: manifestB, Size: int64(len(manifestData)), MediaType: ocispecs.MediaTypeImageManifest,
	}, ociindex.Tag("b")))
	require.NoError(t, export.Close())

	// Reset reuses the export lock after its shared lock has been released.
	require.NoError(t, resetCacheStore(ctx, cs, dir, export.lock))
	require.False(t, export.lock.Locked(), "cleanup must release the reused lock")
	remaining := listDigests(ctx, t, cs)
	require.Len(t, remaining, 6)
	for _, dgst := range []digest.Digest{manifestA, manifestB, configB, layerB} {
		require.Contains(t, remaining, dgst)
	}
	require.NotContains(t, remaining, orphan)
}

func TestCacheExportStoreConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)
	var exports []*cacheExportStore
	var eg errgroup.Group
	for range 2 {
		export := &cacheExportStore{Store: cs, lock: flock.New(filepath.Join(dir, "cache.lock"))}
		t.Cleanup(func() { require.NoError(t, export.Close()) })
		exports = append(exports, export)
		for i := range 8 {
			eg.Go(func() error {
				data := []byte{byte(i)}
				dgst := digest.FromBytes(data)
				return content.WriteBlob(t.Context(), export, dgst.String(), bytes.NewReader(data), ocispecs.Descriptor{Digest: dgst, Size: int64(len(data))})
			})
		}
	}
	require.NoError(t, eg.Wait())
	lock := flock.New(filepath.Join(dir, "cache.lock"))
	defer lock.Close()
	require.NoError(t, exports[0].Close())
	locked, err := lock.TryLock()
	require.NoError(t, err)
	require.False(t, locked, "the second export still needs its blobs")
	require.NoError(t, exports[1].Close())
	locked, err = lock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
}

func TestCacheExportStoreClose(t *testing.T) {
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)
	export := &cacheExportStore{Store: cs, lock: flock.New(filepath.Join(dir, "cache.lock"))}
	t.Cleanup(func() { require.NoError(t, export.Close()) })
	writeBlob(t.Context(), t, export, []byte("layer"))
	require.NoError(t, export.Close())

	// A session handler can outlive session shutdown. It must not reacquire
	// a lock that will never be released after the solve has returned.
	w, err := export.Writer(t.Context(), content.WithRef("late-writer"))
	if w != nil {
		require.NoError(t, w.Close())
	}
	require.ErrorIs(t, err, os.ErrClosed)
	lock := flock.New(filepath.Join(dir, "cache.lock"))
	defer lock.Close()
	locked, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
}

func TestCacheExportStoreWaitForReset(t *testing.T) {
	for _, cancelWriter := range []bool{false, true} {
		name := "release"
		if cancelWriter {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cs, err := contentlocal.NewStore(dir)
			require.NoError(t, err)
			lock := flock.New(filepath.Join(dir, "cache.lock"))
			require.NoError(t, lock.Lock())
			t.Cleanup(func() { require.NoError(t, lock.Close()) })
			export := &cacheExportStore{Store: cs, lock: flock.New(filepath.Join(dir, "cache.lock"))}
			t.Cleanup(func() { require.NoError(t, export.Close()) })
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				done := make(chan error, 1)
				go func() {
					w, err := export.Writer(ctx, content.WithRef("waiting-writer"))
					if err == nil {
						err = w.Close()
					}
					done <- err
				}()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("writer did not wait for reset: %v", err)
				default:
				}
				if cancelWriter {
					cancel(context.Canceled)
					require.NoError(t, export.Close())
					require.ErrorIs(t, <-done, context.Canceled)
				} else {
					require.NoError(t, lock.Unlock())
					require.NoError(t, <-done)
				}
			})
		})
	}
}

type resetLockCheckStore struct {
	content.Store
	check func()
}

func (s resetLockCheckStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	s.check()
	return s.Store.Walk(ctx, fn, filters...)
}

func (s resetLockCheckStore) Delete(ctx context.Context, dgst digest.Digest) error {
	s.check()
	return s.Store.Delete(ctx, dgst)
}

func TestResetCacheStoreHoldsLock(t *testing.T) {
	ctx := t.Context()
	dir, cs, _ := setupCacheStore(ctx, t, []byte(`{}`), nil, "latest")
	orphan := writeBlob(ctx, t, cs, []byte("orphan"))
	lock := flock.New(filepath.Join(dir, "cache.lock"))
	defer lock.Close()
	checks := 0
	store := resetLockCheckStore{Store: cs, check: func() {
		checks++
		locked, err := lock.TryRLock()
		require.NoError(t, err)
		require.False(t, locked, "exports must not start during cleanup")
	}}
	require.NoError(t, resetCacheStore(ctx, store, dir, flock.New(filepath.Join(dir, "cache.lock"))))
	require.Equal(t, 2, checks)
	require.NotContains(t, listDigests(ctx, t, cs), orphan)
	locked, err := lock.TryRLock()
	require.NoError(t, err)
	require.True(t, locked, "cleanup must release the lock")
}

func writeBlob(ctx context.Context, t *testing.T, cs content.Store, data []byte) digest.Digest {
	t.Helper()
	dgst := digest.FromBytes(data)
	desc := ocispecs.Descriptor{Digest: dgst, Size: int64(len(data))}
	err := content.WriteBlob(ctx, cs, dgst.String(), bytes.NewReader(data), desc)
	require.NoError(t, err)
	return dgst
}

func listDigests(ctx context.Context, t *testing.T, cs content.Store) map[digest.Digest]struct{} {
	t.Helper()
	result := map[digest.Digest]struct{}{}
	err := cs.Walk(ctx, func(info content.Info) error {
		result[info.Digest] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	return result
}

// setupCacheStore creates a content store with an index.json and a manifest
// referencing the given config and layers. Returns the store path, content
// store, and manifest digest.
func setupCacheStore(ctx context.Context, t *testing.T, configData []byte, layersData [][]byte, tag string) (string, content.Store, digest.Digest) {
	t.Helper()
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)

	configDgst := writeBlob(ctx, t, cs, configData)

	layers := make([]ocispecs.Descriptor, len(layersData))
	for i, ld := range layersData {
		dgst := writeBlob(ctx, t, cs, ld)
		layers[i] = ocispecs.Descriptor{Digest: dgst, Size: int64(len(ld))}
	}

	manifest := ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config: ocispecs.Descriptor{
			Digest:    configDgst,
			Size:      int64(len(configData)),
			MediaType: "application/vnd.buildkit.cacheconfig.v0",
		},
		Layers: layers,
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestDgst := writeBlob(ctx, t, cs, manifestData)

	idx := ociindex.NewStoreIndex(dir)
	err = idx.Put(ocispecs.Descriptor{
		Digest:    manifestDgst,
		Size:      int64(len(manifestData)),
		MediaType: ocispecs.MediaTypeImageManifest,
	}, ociindex.Tag(tag))
	require.NoError(t, err)

	return dir, cs, manifestDgst
}

func TestResetCacheStoreImageManifest(t *testing.T) {
	ctx := t.Context()
	dir, cs, manifestDgst := setupCacheStore(ctx, t,
		[]byte(`{"test":"config"}`),
		[][]byte{[]byte("layer1-data")},
		"latest",
	)

	// Write orphan blob
	orphanDgst := writeBlob(ctx, t, cs, []byte("orphan-old-layer"))

	// Verify 4 blobs exist
	require.Len(t, listDigests(ctx, t, cs), 4)

	err := resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock")))
	require.NoError(t, err)

	remaining := listDigests(ctx, t, cs)
	require.Len(t, remaining, 3) // manifest + config + layer
	require.Contains(t, remaining, manifestDgst)
	require.NotContains(t, remaining, orphanDgst)
}

func TestResetCacheStoreMultipleTags(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)

	// Manifest 1 (tag=v1)
	config1Dgst := writeBlob(ctx, t, cs, []byte(`{"tag":"v1"}`))
	layer1Dgst := writeBlob(ctx, t, cs, []byte("layer1-only-in-v1"))
	m1 := ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    ocispecs.Descriptor{Digest: config1Dgst, Size: 12, MediaType: "application/vnd.buildkit.cacheconfig.v0"},
		Layers:    []ocispecs.Descriptor{{Digest: layer1Dgst, Size: 17}},
	}
	m1Data, err := json.Marshal(m1)
	require.NoError(t, err)
	m1Dgst := writeBlob(ctx, t, cs, m1Data)

	// Manifest 2 (tag=v2)
	config2Dgst := writeBlob(ctx, t, cs, []byte(`{"tag":"v2"}`))
	layer2Dgst := writeBlob(ctx, t, cs, []byte("layer2-only-in-v2"))
	m2 := ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    ocispecs.Descriptor{Digest: config2Dgst, Size: 12, MediaType: "application/vnd.buildkit.cacheconfig.v0"},
		Layers:    []ocispecs.Descriptor{{Digest: layer2Dgst, Size: 17}},
	}
	m2Data, err := json.Marshal(m2)
	require.NoError(t, err)
	m2Dgst := writeBlob(ctx, t, cs, m2Data)

	// Orphan blob
	orphanDgst := writeBlob(ctx, t, cs, []byte("orphan-blob"))

	// Write index.json with both tags
	idx := ociindex.NewStoreIndex(dir)
	require.NoError(t, idx.Put(ocispecs.Descriptor{Digest: m1Dgst, Size: int64(len(m1Data)), MediaType: ocispecs.MediaTypeImageManifest}, ociindex.Tag("v1")))
	require.NoError(t, idx.Put(ocispecs.Descriptor{Digest: m2Dgst, Size: int64(len(m2Data)), MediaType: ocispecs.MediaTypeImageManifest}, ociindex.Tag("v2")))

	require.Len(t, listDigests(ctx, t, cs), 7)

	err = resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock")))
	require.NoError(t, err)

	remaining := listDigests(ctx, t, cs)
	require.Len(t, remaining, 6)
	require.Contains(t, remaining, m1Dgst)
	require.Contains(t, remaining, config1Dgst)
	require.Contains(t, remaining, layer1Dgst)
	require.Contains(t, remaining, m2Dgst)
	require.Contains(t, remaining, config2Dgst)
	require.Contains(t, remaining, layer2Dgst)
	require.NotContains(t, remaining, orphanDgst)
}

func TestResetCacheStoreNoOrphans(t *testing.T) {
	ctx := t.Context()
	dir, cs, _ := setupCacheStore(ctx, t,
		[]byte(`{"test":"config"}`),
		nil,
		"latest",
	)

	err := resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock")))
	require.NoError(t, err)

	require.Len(t, listDigests(ctx, t, cs), 2) // manifest + config
}

func TestResetCacheStoreNestedIndex(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)

	// Build a manifest with config + layer
	configData := []byte(`{"nested":"config"}`)
	layerData := []byte("nested-layer-data")
	configDgst := writeBlob(ctx, t, cs, configData)
	layerDgst := writeBlob(ctx, t, cs, layerData)
	manifest := ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    ocispecs.Descriptor{Digest: configDgst, Size: int64(len(configData)), MediaType: "application/vnd.buildkit.cacheconfig.v0"},
		Layers:    []ocispecs.Descriptor{{Digest: layerDgst, Size: int64(len(layerData)), MediaType: ocispecs.MediaTypeImageLayerGzip}},
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestDgst := writeBlob(ctx, t, cs, manifestData)

	// A cache config blob referenced directly by the sub-index (no manifest wrapper)
	cacheConfigData := []byte(`{"cache":"config"}`)
	cacheConfigDgst := writeBlob(ctx, t, cs, cacheConfigData)

	// Build a sub-index that references both the manifest and the cache config directly
	subIndex := ocispecs.Index{
		MediaType: ocispecs.MediaTypeImageIndex,
		Manifests: []ocispecs.Descriptor{
			{Digest: manifestDgst, Size: int64(len(manifestData)), MediaType: ocispecs.MediaTypeImageManifest},
			{Digest: cacheConfigDgst, Size: int64(len(cacheConfigData)), MediaType: "application/vnd.buildkit.cacheconfig.v0"},
		},
	}
	subIndexData, err := json.Marshal(subIndex)
	require.NoError(t, err)
	subIndexDgst := writeBlob(ctx, t, cs, subIndexData)

	orphanDgst := writeBlob(ctx, t, cs, []byte("orphan-data"))

	// Top-level index.json references the sub-index
	idx := ociindex.NewStoreIndex(dir)
	require.NoError(t, idx.Put(ocispecs.Descriptor{
		Digest:    subIndexDgst,
		Size:      int64(len(subIndexData)),
		MediaType: ocispecs.MediaTypeImageIndex,
	}, ociindex.Tag("latest")))

	require.Len(t, listDigests(ctx, t, cs), 6) // sub-index + manifest + config + layer + cacheConfig + orphan

	err = resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock")))
	require.NoError(t, err)

	remaining := listDigests(ctx, t, cs)
	require.Len(t, remaining, 5) // sub-index + manifest + config + layer + cacheConfig
	require.Contains(t, remaining, subIndexDgst)
	require.Contains(t, remaining, manifestDgst)
	require.Contains(t, remaining, configDgst)
	require.Contains(t, remaining, layerDgst)
	require.Contains(t, remaining, cacheConfigDgst)
	require.NotContains(t, remaining, orphanDgst)
}

func TestResetCacheStoreDockerMediaTypes(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	cs, err := contentlocal.NewStore(dir)
	require.NoError(t, err)

	// Build a manifest using Docker schema2 media types
	configData := []byte(`{"docker":"config"}`)
	layerData := []byte("docker-layer")
	configDgst := writeBlob(ctx, t, cs, configData)
	layerDgst := writeBlob(ctx, t, cs, layerData)
	manifest := ocispecs.Manifest{
		MediaType: images.MediaTypeDockerSchema2Manifest,
		Config:    ocispecs.Descriptor{Digest: configDgst, Size: int64(len(configData)), MediaType: images.MediaTypeDockerSchema2Config},
		Layers:    []ocispecs.Descriptor{{Digest: layerDgst, Size: int64(len(layerData)), MediaType: images.MediaTypeDockerSchema2LayerGzip}},
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)
	manifestDgst := writeBlob(ctx, t, cs, manifestData)

	// Build a manifest list using Docker schema2 media type
	manifestList := ocispecs.Index{
		MediaType: images.MediaTypeDockerSchema2ManifestList,
		Manifests: []ocispecs.Descriptor{{
			Digest:    manifestDgst,
			Size:      int64(len(manifestData)),
			MediaType: images.MediaTypeDockerSchema2Manifest,
		}},
	}
	manifestListData, err := json.Marshal(manifestList)
	require.NoError(t, err)
	manifestListDgst := writeBlob(ctx, t, cs, manifestListData)

	orphanDgst := writeBlob(ctx, t, cs, []byte("docker-orphan"))

	idx := ociindex.NewStoreIndex(dir)
	require.NoError(t, idx.Put(ocispecs.Descriptor{
		Digest:    manifestListDgst,
		Size:      int64(len(manifestListData)),
		MediaType: images.MediaTypeDockerSchema2ManifestList,
	}, ociindex.Tag("latest")))

	require.Len(t, listDigests(ctx, t, cs), 5)

	err = resetCacheStore(ctx, cs, dir, flock.New(filepath.Join(dir, "cache.lock")))
	require.NoError(t, err)

	remaining := listDigests(ctx, t, cs)
	require.Len(t, remaining, 4) // manifest list + manifest + config + layer
	require.Contains(t, remaining, manifestListDgst)
	require.Contains(t, remaining, manifestDgst)
	require.Contains(t, remaining, configDgst)
	require.Contains(t, remaining, layerDgst)
	require.NotContains(t, remaining, orphanDgst)
}

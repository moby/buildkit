package ociindex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptyDir(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreIndex(dir)
	idx, err := store.Read()
	require.Error(t, err)
	assert.Nil(t, idx)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadIndex(t *testing.T) {
	dir := t.TempDir()
	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			randDescriptor("foo"),
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	readIdx, err := store.Read()
	require.NoError(t, err)
	assert.Len(t, readIdx.Manifests, 1)

	assert.Equal(t, idx.Manifests[0], readIdx.Manifests[0])
}

func TestReadByTag(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	two := randDescriptor("bar")
	three := randDescriptor("baz")

	const refName = "org.opencontainers.image.ref.name"

	two.Annotations = map[string]string{
		refName: "ver1",
	}
	three.Annotations = map[string]string{
		refName: "ver2",
	}

	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			one,
			two,
			three,
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	desc, err := store.Get("ver1")
	require.NoError(t, err)

	assert.Equal(t, *desc, two)

	desc, err = store.Get("ver3")
	require.NoError(t, err)
	assert.Nil(t, desc)
}

func TestWriteSingleDescriptor(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreIndex(dir)

	desc := randDescriptor("foo")
	err := store.Put("", desc)
	require.NoError(t, err)

	readDesc, err := store.GetSingle()
	require.NoError(t, err)
	assert.Equal(t, desc, *readDesc)
}

func TestAddDescriptor(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	two := randDescriptor("bar")

	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			one,
			two,
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	three := randDescriptor("baz")
	err = store.Put("", three)
	require.NoError(t, err)

	readIdx, err := store.Read()
	require.NoError(t, err)

	assert.Len(t, readIdx.Manifests, 3)
	assert.Equal(t, one, readIdx.Manifests[0])
	assert.Equal(t, two, readIdx.Manifests[1])
	assert.Equal(t, three, readIdx.Manifests[2])

	// store.Put also sets defaults for MediaType and SchemaVersion
	assert.Equal(t, ocispecs.MediaTypeImageIndex, readIdx.MediaType)
	assert.Equal(t, 2, readIdx.SchemaVersion)
}

func TestAddDescriptorWithTag(t *testing.T) {
	dir := t.TempDir()

	one := randDescriptor("foo")
	two := randDescriptor("bar")

	idx := ocispecs.Index{
		Manifests: []ocispecs.Descriptor{
			one,
			two,
		},
	}
	dt, err := json.Marshal(idx)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(dir, "index.json"), dt, 0644)
	require.NoError(t, err)

	store := NewStoreIndex(dir)
	three := randDescriptor("baz")
	err = store.Put("ver1", three)
	require.NoError(t, err)

	desc, err := store.Get("ver1")
	require.NoError(t, err)

	assert.Equal(t, three.Digest, desc.Digest)
	assert.Equal(t, three.Size, desc.Size)
	assert.Equal(t, three.MediaType, desc.MediaType)

	assert.Equal(t, "ver1", desc.Annotations["org.opencontainers.image.ref.name"])

	readIdx, err := store.Read()
	require.NoError(t, err)

	assert.Len(t, readIdx.Manifests, 3)
	assert.Equal(t, one, readIdx.Manifests[0])
	assert.Equal(t, two, readIdx.Manifests[1])
	assert.Equal(t, *desc, readIdx.Manifests[2])
}

func randDescriptor(seed string) ocispecs.Descriptor {
	dgst := digest.FromBytes([]byte(seed))
	return ocispecs.Descriptor{
		MediaType: "application/vnd.test.descriptor+json",
		Digest:    dgst,
		Size:      int64(len(seed)),
	}
}

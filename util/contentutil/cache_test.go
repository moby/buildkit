package contentutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/plugins/content/local"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	data      map[digest.Digest][]byte
	calls     int
	refs      map[digest.Digest][]ocispecs.Descriptor
	refsCalls int
}

func newStubProvider() *stubProvider {
	return &stubProvider{}
}

func (p *stubProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	p.calls++
	b, ok := p.data[desc.Digest]
	if !ok {
		return nil, errors.Errorf("not found: %s", desc.Digest.String())
	}
	return newBuf(b), nil
}

func (p *stubProvider) FetchReferrers(ctx context.Context, dgst digest.Digest, opts ...remotes.FetchReferrersOpt) ([]ocispecs.Descriptor, error) {
	p.refsCalls++
	refs, ok := p.refs[dgst]
	if !ok {
		return nil, nil
	}
	return refs, nil
}

func (p *stubProvider) add(dt []byte) ocispecs.Descriptor {
	if p.data == nil {
		p.data = make(map[digest.Digest][]byte)
	}
	dgst := digest.FromBytes(dt)
	p.data[dgst] = dt
	return ocispecs.Descriptor{
		Digest:       dgst,
		Size:         int64(len(dt)),
		ArtifactType: readArtifactType(dt),
	}
}

func (p *stubProvider) addReferrer(target digest.Digest, dt []byte) ocispecs.Descriptor {
	if _, ok := p.data[target]; !ok {
		panic("target not found") // this is test only helper
	}
	if p.refs == nil {
		p.refs = make(map[digest.Digest][]ocispecs.Descriptor)
	}
	old, ok := p.refs[target]
	if !ok {
		old = []ocispecs.Descriptor{}
	}
	desc := p.add(dt)
	p.refs[target] = append(old, desc)
	return desc
}

type buf struct {
	*bytes.Reader
}

func (r *buf) Close() error { return nil }

func newBuf(b []byte) *buf {
	return &buf{
		Reader: bytes.NewReader(b),
	}
}

func stubManifest(t *testing.T, name, artifactType string) []byte {
	manif := ocispecs.Manifest{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:    ocispecs.MediaTypeImageManifest,
		ArtifactType: artifactType,
		Annotations: map[string]string{
			"test.name": name,
		},
	}
	dt, err := json.Marshal(manif)
	require.NoError(t, err)
	return dt
}

func TestReferrersProviderBuffer(t *testing.T) {
	ctx := t.Context()
	buf := NewBuffer()
	rp := newStubProvider()

	rootDigest := digest.FromString("root")
	rw, err := content.OpenWriter(ctx, buf)
	require.NoError(t, err)
	err = content.Copy(ctx, rw, bytes.NewReader([]byte("root")), 4, rootDigest)
	require.NoError(t, err)

	hello := rp.add([]byte("hello"))
	world := rp.add([]byte("world!"))

	rpb := ReferrersProviderWithBuffer(rp, buf, "")

	ra, err := rpb.ReaderAt(ctx, hello)
	require.NoError(t, err)

	require.Equal(t, hello.Size, ra.Size())
	ra.Close()

	ra, err = buf.ReaderAt(ctx, hello)
	require.NoError(t, err)

	b := make([]byte, hello.Size)
	n, err := ra.ReadAt(b, 0)
	require.NoError(t, err)
	require.Equal(t, int(hello.Size), n)
	require.Equal(t, []byte("hello"), b)
	ra.Close()

	require.Equal(t, 1, rp.calls)

	ra, err = rpb.ReaderAt(ctx, world)
	require.NoError(t, err)
	require.Equal(t, world.Size, ra.Size())
	ra.Close()

	ra, err = buf.ReaderAt(ctx, world)
	require.NoError(t, err)

	b = make([]byte, world.Size)
	n, err = ra.ReadAt(b, 0)
	require.NoError(t, err)
	require.Equal(t, int(world.Size), n)
	require.Equal(t, []byte("world!"), b)
	ra.Close()

	require.Equal(t, 2, rp.calls)

	// second read should hit cache
	ra, err = rpb.ReaderAt(ctx, hello)
	require.NoError(t, err)
	require.Equal(t, hello.Size, ra.Size())
	ra.Close()

	require.Equal(t, 2, rp.calls)

	err = rpb.SetGCLabels(ctx, ocispecs.Descriptor{
		Digest: rootDigest,
	})
	require.NoError(t, err)

	info, err := buf.Info(ctx, rootDigest)
	require.NoError(t, err)
	require.Equal(t, rootDigest, info.Digest)
	require.Equal(t, int64(4), info.Size)

	labels := info.Labels
	require.Equal(t, 2, len(labels))
	pfx := hello.Digest.Hex()[:12]
	lbl1, ok := labels["containerd.io/gc.ref.content.buildkit."+pfx]
	require.True(t, ok)
	require.Equal(t, hello.Digest.String(), lbl1)

	pfx = world.Digest.Hex()[:12]
	lbl2, ok := labels["containerd.io/gc.ref.content.buildkit."+pfx]
	require.True(t, ok)
	require.Equal(t, world.Digest.String(), lbl2)
}

func TestReferrersProviderRefsBuffer(t *testing.T) {
	ctx := t.Context()
	buf := NewBuffer()
	rp := newStubProvider()

	rootDigest := digest.FromString("root")
	rw, err := content.OpenWriter(ctx, buf)
	require.NoError(t, err)
	err = content.Copy(ctx, rw, bytes.NewReader([]byte("root")), 4, rootDigest)
	require.NoError(t, err)

	hello := rp.add([]byte("hello"))
	r1 := stubManifest(t, "ref1", "type")
	ref1 := rp.addReferrer(hello.Digest, r1)
	r2 := stubManifest(t, "ref2", "type")
	ref2 := rp.addReferrer(hello.Digest, r2)
	world := rp.add([]byte("world!"))

	rpb := ReferrersProviderWithBuffer(rp, buf, "repo1")

	ra, err := rpb.ReaderAt(ctx, hello)
	require.NoError(t, err)
	ra.Close()

	refs, err := rpb.FetchReferrers(ctx, hello.Digest, remotes.WithReferrerArtifactTypes("type"))
	require.NoError(t, err)
	require.Equal(t, 2, len(refs))
	require.Contains(t, refs, ref1)
	require.Contains(t, refs, ref2)

	require.Equal(t, 1, rp.refsCalls)

	ra, err = rpb.ReaderAt(ctx, ref1)
	require.NoError(t, err)
	ra.Close()

	ra, err = rpb.ReaderAt(ctx, ref2)
	require.NoError(t, err)
	ra.Close()

	err = rpb.SetGCLabels(ctx, ocispecs.Descriptor{
		Digest: rootDigest,
	})
	require.NoError(t, err)

	refs, err = rpb.FetchReferrers(ctx, hello.Digest, remotes.WithReferrerArtifactTypes("type"))
	require.NoError(t, err)
	require.Equal(t, 2, len(refs))
	require.Contains(t, []digest.Digest{ref1.Digest, ref2.Digest}, refs[0].Digest)
	require.Contains(t, []digest.Digest{ref1.Digest, ref2.Digest}, refs[1].Digest)

	require.Equal(t, 1, rp.refsCalls)

	info, err := buf.Info(ctx, hello.Digest)
	require.NoError(t, err)
	labels := info.Labels

	require.Equal(t, 2, len(labels))
	pfx := ref1.Digest.Hex()[:12]
	lbl1, ok := labels["containerd.io/gc.ref.content.buildkit.refs."+pfx]
	require.True(t, ok)
	require.Equal(t, ref1.Digest.String(), lbl1)

	pfx = ref2.Digest.Hex()[:12]
	lbl2, ok := labels["containerd.io/gc.ref.content.buildkit.refs."+pfx]
	require.True(t, ok)
	require.Equal(t, ref2.Digest.String(), lbl2)

	// tests for empty refs calls
	rpb = ReferrersProviderWithBuffer(rp, buf, "repo1")

	ra, err = rpb.ReaderAt(ctx, world)
	require.NoError(t, err)
	ra.Close()

	refs, err = rpb.FetchReferrers(ctx, world.Digest, remotes.WithReferrerArtifactTypes("type"))
	require.NoError(t, err)
	require.Equal(t, 0, len(refs))

	require.Equal(t, 2, rp.refsCalls)

	err = rpb.SetGCLabels(ctx, ocispecs.Descriptor{
		Digest: rootDigest,
	})
	require.NoError(t, err)

	refs, err = rpb.FetchReferrers(ctx, world.Digest, remotes.WithReferrerArtifactTypes("type"))
	require.NoError(t, err)
	require.Equal(t, 0, len(refs))

	require.Equal(t, 2, rp.refsCalls)

	info, err = buf.Info(ctx, world.Digest)
	require.NoError(t, err)
	labels = info.Labels
	require.Equal(t, 1, len(labels))

	lbl1, ok = labels["buildkit/refs.null"]
	require.True(t, ok)
	require.Equal(t, "repo1", lbl1)
}

// TestReferrersProviderBufferCancelledReadDoesNotPoison verifies a cancelled or
// failed read does not poison later attempts for the same digest ref.
func TestReferrersProviderBufferCancelledReadDoesNotPoison(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	data := []byte("0123456789abcdef")
	desc := ocispecs.Descriptor{
		Digest: digest.FromBytes(data),
		Size:   int64(len(data)),
	}

	rp := &cancelOnceProvider{data: data}

	rpb := ReferrersProviderWithBuffer(rp, store, "repo1")

	_, err = rpb.ReaderAt(ctx, desc)
	require.ErrorIs(t, err, context.Canceled)

	ra, err := rpb.ReaderAt(ctx, desc)
	require.NoError(t, err)
	ra.Close()

	got, err := content.ReadBlob(ctx, store, desc)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Equal(t, 2, rp.calls)
	require.Equal(t, 2, rp.closeCalls)
}

// TestReferrersProviderBufferResumedIngestOffset verifies resumed ingests are
// reset before copying to avoid short-read failures on reused refs.
func TestReferrersProviderBufferResumedIngestOffset(t *testing.T) {
	ctx := t.Context()
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)

	data := []byte("0123456789abcdef")
	rp := newStubProvider()
	desc := rp.add(data)

	// Simulate a pre-existing resumable ingest for the same ref with offset=size.
	// This can happen when an earlier attempt closed an ingest without commit.
	ref := desc.Digest.String()
	cw, err := content.OpenWriter(ctx, store, content.WithRef(ref), content.WithDescriptor(desc))
	require.NoError(t, err)
	_, err = cw.Write(data)
	require.NoError(t, err)
	require.NoError(t, cw.Close())

	rpb := ReferrersProviderWithBuffer(rp, store, "repo1")

	ra, err := rpb.ReaderAt(ctx, desc)
	require.NoError(t, err)
	ra.Close()

	got, err := content.ReadBlob(ctx, store, desc)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

type sizedReadCloser struct {
	io.ReaderAt
	size    int64
	closed  *bool
	onClose func()
}

func (r *sizedReadCloser) Close() error {
	if r.onClose != nil {
		r.onClose()
	}
	if r.closed != nil {
		*r.closed = true
	}
	return nil
}

func (r *sizedReadCloser) Size() int64 {
	return r.size
}

// cancelOnceProvider simulates a provider whose first read is canceled and
// whose subsequent reads succeed with full content.
type cancelOnceProvider struct {
	data       []byte
	calls      int
	closeCalls int
}

func (p *cancelOnceProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	p.calls++
	if desc.Digest != digest.FromBytes(p.data) {
		return nil, errors.Errorf("unexpected digest: %s", desc.Digest)
	}
	if p.calls == 1 {
		return &sizedReadCloser{
			ReaderAt: errReaderAt{err: context.Canceled},
			size:     int64(len(p.data)),
			onClose:  func() { p.closeCalls++ },
		}, nil
	}
	return &sizedReadCloser{
		ReaderAt: bytes.NewReader(p.data),
		size:     int64(len(p.data)),
		onClose:  func() { p.closeCalls++ },
	}, nil
}

func (p *cancelOnceProvider) FetchReferrers(ctx context.Context, dgst digest.Digest, opts ...remotes.FetchReferrersOpt) ([]ocispecs.Descriptor, error) {
	return nil, nil
}

type errReaderAt struct {
	err error
}

func (r errReaderAt) ReadAt([]byte, int64) (int, error) {
	return 0, r.err
}

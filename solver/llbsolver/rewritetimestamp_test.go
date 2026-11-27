package llbsolver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func writeLayer(t *testing.T, cs content.Store, mtime time.Time, extraLabels map[string]string) ocispecs.Descriptor {
	t.Helper()
	tarBuf := &bytes.Buffer{}
	tw := tar.NewWriter(tarBuf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0755, ModTime: mtime}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "dir/foo", Typeflag: tar.TypeReg, Mode: 0644, Size: 3, ModTime: time.Unix(10, 0)}))
	_, err := tw.Write([]byte("foo"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	diffID := digest.FromBytes(tarBuf.Bytes())

	gzBuf := &bytes.Buffer{}
	gw := gzip.NewWriter(gzBuf)
	_, err = io.Copy(gw, tarBuf)
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	desc := ocispecs.Descriptor{
		MediaType:   ocispecs.MediaTypeImageLayerGzip,
		Digest:      digest.FromBytes(gzBuf.Bytes()),
		Size:        int64(gzBuf.Len()),
		Annotations: map[string]string{labels.LabelUncompressed: diffID.String()},
	}
	lbls := map[string]string{labels.LabelUncompressed: diffID.String()}
	maps.Copy(lbls, extraLabels)
	require.NoError(t, content.WriteBlob(t.Context(), cs, desc.Digest.String(), bytes.NewReader(gzBuf.Bytes()), desc, content.WithLabels(lbls)))
	return desc
}

func readLayerMtimes(t *testing.T, cs content.Store, desc ocispecs.Descriptor) map[string]int64 {
	t.Helper()
	ra, err := cs.ReaderAt(t.Context(), desc)
	require.NoError(t, err)
	defer ra.Close()
	gr, err := gzip.NewReader(content.NewReader(ra))
	require.NoError(t, err)
	tr := tar.NewReader(gr)
	m := map[string]int64{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		m[hdr.Name] = hdr.ModTime.Unix()
	}
	return m
}

func TestTimestampRewriter(t *testing.T) {
	cs, err := local.NewLabeledStore(t.TempDir(), newMemoryLabelStore())
	require.NoError(t, err)

	epoch := time.Unix(42, 0)
	now := time.Now()

	built := writeLayer(t, cs, now, nil)
	clean := writeLayer(t, cs, epoch, nil)
	pulled := writeLayer(t, cs, now.Add(time.Second), map[string]string{distributionSourceLabelPrefix + "docker.io": "library/alpine"})
	lazy := ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageLayerGzip, Digest: digest.FromString("lazy"), Size: 4}

	r := newTimestampRewriter(epoch, compression.New(compression.Gzip))
	remote := &solver.Remote{Descriptors: []ocispecs.Descriptor{pulled, lazy, built, clean}, Provider: cs}
	out, err := r.rewriteRemote(t.Context(), cs, remote)
	require.NoError(t, err)
	require.Len(t, out.Descriptors, 4)

	require.Equal(t, pulled, out.Descriptors[0], "layers pulled from a registry are kept")
	require.Equal(t, lazy, out.Descriptors[1], "lazy layers are kept")
	require.Equal(t, clean, out.Descriptors[3], "layers without anything to clamp are kept")

	rewritten := out.Descriptors[2]
	require.NotEqual(t, built.Digest, rewritten.Digest)
	require.Equal(t, "42", rewritten.Annotations["buildkit/rewritten-timestamp"])
	require.Equal(t, map[string]int64{"dir/": 42, "dir/foo": 10}, readLayerMtimes(t, cs, rewritten))

	// the same layer created at another time is rewritten to the same blob
	built2 := writeLayer(t, cs, now.Add(time.Hour), nil)
	out2, err := r.rewriteRemote(t.Context(), cs, &solver.Remote{Descriptors: []ocispecs.Descriptor{built2}, Provider: cs})
	require.NoError(t, err)
	require.Equal(t, rewritten.Digest, out2.Descriptors[0].Digest)

	// nothing to rewrite: the remote is returned as is
	out3, err := r.rewriteRemote(t.Context(), cs, &solver.Remote{Descriptors: []ocispecs.Descriptor{clean}, Provider: cs})
	require.NoError(t, err)
	require.Equal(t, clean, out3.Descriptors[0])
}

type memoryLabelStore struct {
	mu     sync.Mutex
	labels map[digest.Digest]map[string]string
}

func newMemoryLabelStore() *memoryLabelStore {
	return &memoryLabelStore{labels: map[digest.Digest]map[string]string{}}
}

func (s *memoryLabelStore) Get(d digest.Digest) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.labels[d]), nil
}

func (s *memoryLabelStore) Set(d digest.Digest, l map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels[d] = maps.Clone(l)
	return nil
}

func (s *memoryLabelStore) Update(d digest.Digest, update map[string]string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.labels[d]
	if l == nil {
		l = map[string]string{}
	}
	for k, v := range update {
		if v == "" {
			delete(l, k)
		} else {
			l[k] = v
		}
	}
	s.labels[d] = l
	return maps.Clone(l), nil
}

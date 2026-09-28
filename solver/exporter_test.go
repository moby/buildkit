package solver

import (
	"slices"
	"testing"
	"time"

	"github.com/moby/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestNeedsLocalResult(t *testing.T) {
	gzipLayers := &Remote{Descriptors: []ocispecs.Descriptor{
		{MediaType: ocispecs.MediaTypeImageLayerGzip},
		{MediaType: ocispecs.MediaTypeImageLayerGzip},
	}}
	zstdLayers := &Remote{Descriptors: []ocispecs.Descriptor{
		{MediaType: ocispecs.MediaTypeImageLayerZstd},
	}}
	// e.g. zstd layers built on top of a gzip base image.
	mixedLayers := &Remote{Descriptors: []ocispecs.Descriptor{
		{MediaType: ocispecs.MediaTypeImageLayerGzip},
		{MediaType: ocispecs.MediaTypeImageLayerZstd},
	}}
	withCompression := func(c compression.Config) CacheExportOpt {
		return CacheExportOpt{CompressionOpt: &c}
	}

	tests := []struct {
		name   string
		remote *Remote
		opt    CacheExportOpt
		needed bool
	}{
		{name: "no remote", opt: withCompression(compression.New(compression.Gzip)), needed: true},
		{name: "no compression option", remote: gzipLayers},
		{name: "gzip", remote: gzipLayers, opt: withCompression(compression.New(compression.Gzip))},
		{name: "zstd requested for gzip layers", remote: gzipLayers, opt: withCompression(compression.New(compression.Zstd))},
		{name: "zstd requested for mixed layers", remote: mixedLayers, opt: withCompression(compression.New(compression.Zstd))},
		{name: "forced zstd for zstd layers", remote: zstdLayers, opt: withCompression(compression.New(compression.Zstd).SetForce(true))},
		{name: "forced zstd for gzip layers", remote: gzipLayers, opt: withCompression(compression.New(compression.Zstd).SetForce(true)), needed: true},
		{name: "forced zstd for mixed layers", remote: mixedLayers, opt: withCompression(compression.New(compression.Zstd).SetForce(true)), needed: true},
		{name: "forced gzip", remote: gzipLayers, opt: withCompression(compression.New(compression.Gzip).SetForce(true)), needed: true},
		{name: "forced estargz", remote: gzipLayers, opt: withCompression(compression.New(compression.EStargz).SetForce(true)), needed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.needed, needsLocalResult(tt.remote, tt.opt))
		})
	}
}

func TestCompareCacheRecord(t *testing.T) {
	now := time.Now()
	a := &CacheRecord{CreatedAt: now, Priority: 1}
	b := &CacheRecord{CreatedAt: now, Priority: 2}
	c := &CacheRecord{CreatedAt: now.Add(1 * time.Second), Priority: 1}
	d := &CacheRecord{CreatedAt: now.Add(-1 * time.Second), Priority: 1}

	records := []*CacheRecord{b, nil, d, a, c, nil}
	slices.SortFunc(records, compareCacheRecord)

	names := map[*CacheRecord]string{
		a:   "a",
		b:   "b",
		c:   "c",
		d:   "d",
		nil: "nil",
	}
	var got []string
	for _, r := range records {
		got = append(got, names[r])
	}
	want := []string{"c", "a", "b", "d", "nil", "nil"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected order: got %v, want %v", got, want)
	}
}

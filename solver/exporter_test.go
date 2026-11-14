package solver

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

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

func TestExporterRemoteAvailability(t *testing.T) {
	remote := &Remote{
		Descriptors: []ocispecs.Descriptor{{
			Digest:    dgst("layer"),
			MediaType: ocispecs.MediaTypeImageLayerGzip,
		}},
		Provider: &exporterTestProvider{},
	}

	for _, tc := range []struct {
		name        string
		remotes     []*Remote
		infoErr     error
		wantLoads   int
		wantInfo    int
		wantResults int
		wantErr     bool
	}{
		{
			name:        "available remote",
			remotes:     []*Remote{remote},
			wantInfo:    1,
			wantResults: 1,
		},
		{
			name:        "available remote with alternate",
			remotes:     []*Remote{remote, remote},
			wantInfo:    1,
			wantResults: 2,
		},
		{
			name:     "missing descriptor",
			remotes:  []*Remote{remote},
			infoErr:  cerrdefs.ErrNotFound,
			wantInfo: 1,
		},
		{
			name:     "provider error",
			remotes:  []*Remote{remote},
			infoErr:  errors.New("provider unavailable"),
			wantInfo: 1,
			wantErr:  true,
		},
		{
			name:        "no compatible remote",
			wantLoads:   1,
			wantResults: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &exporterTestProvider{err: tc.infoErr}
			tcRemote := *remote
			tcRemote.Provider = provider
			remotes := slices.Clone(tc.remotes)
			if len(remotes) > 0 {
				remotes[0] = &tcRemote
			}
			base := NewInMemoryResultStorage()
			store := &exporterTestResultStore{
				CacheResultStorage: base,
				remotes:            remotes,
			}
			cm := NewCacheManager(t.Context(), identity.NewID(), NewInMemoryCacheStorage(), store)
			ck, err := cm.Save(NewCacheKey(dgst("key"), dgst("vertex"), 0), testResult("result"), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			target := &exporterTestTarget{}
			comp := compression.Config{Type: compression.Gzip, Force: true}
			_, err = ck.Exporter.ExportTo(t.Context(), target, CacheExportOpt{
				ExportRoots:    true,
				CompressionOpt: &comp,
				ResolveRemotes: func(context.Context, Result) ([]*Remote, error) {
					return []*Remote{&tcRemote}, nil
				},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ExportTo error = %v, want error %t", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if store.loadCalls != tc.wantLoads {
				t.Fatalf("Load calls = %d, want %d", store.loadCalls, tc.wantLoads)
			}
			if provider.infoCalls != tc.wantInfo {
				t.Fatalf("provider Info calls = %d, want %d", provider.infoCalls, tc.wantInfo)
			}
			gotResults := 0
			for _, results := range target.results {
				gotResults += len(results)
			}
			if gotResults != tc.wantResults {
				t.Fatalf("exported results = %d, want %d", gotResults, tc.wantResults)
			}
		})
	}
}

type exporterTestResultStore struct {
	CacheResultStorage
	remotes   []*Remote
	loadCalls int
}

func (s *exporterTestResultStore) Load(ctx context.Context, res CacheResult) (Result, error) {
	s.loadCalls++
	return s.CacheResultStorage.Load(ctx, res)
}

func (s *exporterTestResultStore) LoadRemotes(context.Context, CacheResult, *compression.Config, session.Group) ([]*Remote, error) {
	return s.remotes, nil
}

type exporterTestProvider struct {
	content.InfoReaderProvider
	err       error
	infoCalls int
}

func (p *exporterTestProvider) Info(context.Context, digest.Digest) (content.Info, error) {
	p.infoCalls++
	return content.Info{}, p.err
}

type exporterTestTarget struct {
	results [][]CacheExportResult
}

func (t *exporterTestTarget) Add(_ digest.Digest, _ [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error) {
	t.results = append(t.results, slices.Clone(results))
	return &CacheExporterRecordBase{}, true, nil
}

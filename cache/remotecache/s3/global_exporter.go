package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// ResolveGlobalCacheExporterFunc exports partial cache layers for each step in
// s3, disregarding any repo name. Meaning that for the same bucket url and
// prefix, cache will be shared.
func ResolveGlobalCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, _ session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, err
		}

		s3Client, err := newS3Client(ctx, config)
		if err != nil {
			return nil, err
		}
		cc := v1.NewCacheChains()
		return &globalExporter{
			t:        cc,
			chains:   cc,
			s3Client: s3Client,
			config:   config,
		}, nil
	}
}

type globalExporter struct {
	t        solver.CacheExporterTarget
	chains   *v1.CacheChains
	s3Client *s3Client
	config   Config
}

func (e *globalExporter) Add(dgst digest.Digest) solver.CacheExporterRecord {
	return e.t.Add(dgst)
}

func (e *globalExporter) Visit(i interface{}) {
	e.t.Visit(i)
}

func (e *globalExporter) Visited(i interface{}) bool {
	return e.t.Visited(i)
}

func (*globalExporter) Name() string {
	return "exporting global cache manifests to s3 global"
}

func (e *globalExporter) Config() remotecache.Config {
	return remotecache.Config{
		Compression: compression.New(compression.Default),
	}
}

func (e *globalExporter) Finalize(ctx context.Context) (map[string]string, error) {
	cacheConfig, desc, err := e.chains.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	s3Exporter := exporter{
		s3Client: e.s3Client,
		config:   e.config,
	}

	if err := s3Exporter.finalizeBlobs(ctx, cacheConfig, desc); err != nil {
		return nil, err
	}

	files, err := MarshallSplitManifests(cacheConfig)
	if err != nil {
		return nil, err
	}

	g, ctx := errgroup.WithContext(ctx)
	done := progress.OneOff(ctx, fmt.Sprintf("storing %d cache manifests", len(files)))
	for filename, ctt := range files {
		filename, ctt := filename, ctt // something something go and parallelism.
		g.Go(func() error {
			var dt []byte
			// links are empty files and never read, we store nothing instead of
			// storing `null`
			if ctt != nil {
				dt, err = json.Marshal(ctt)
				if err != nil {
					return errors.Wrap(err, "error encoding cache manifest")
				}
			}

			key := e.s3Client.prefix + filename

			if err := e.s3Client.saveMutable(ctx, key, dt); err != nil {
				return errors.Wrap(err, "error writing cache manifest")
			}
			return nil
		})
	}
	return nil, done(g.Wait())
}

func MarshallSplitManifests(v1cfg *v1.CacheConfig) (artifacts map[string]interface{}, err error) {
	artifacts = map[string]interface{}{}
	cache := map[int]*GlobalCacheRecord{}
	for i := range v1cfg.Records {
		_, err := marshalRecord(v1cfg, artifacts, i, cache)
		if err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

func marshalRecord(v1cfg *v1.CacheConfig, artifacts map[string]interface{}, idx int, cache map[int]*GlobalCacheRecord) (*GlobalCacheRecord, error) {
	if r, ok := cache[idx]; ok {
		if r == nil {
			return nil, errors.Errorf("invalid looping record")
		}
		return r, nil
	}
	if idx < 0 || idx >= len(v1cfg.Records) {
		return nil, errors.Errorf("invalid record ID: %d", idx)
	}
	record := v1cfg.Records[idx]
	r := &GlobalCacheRecord{
		ID:     record.Digest,
		Digest: record.Digest,
	}

	// digests can be duplicate in a cache config, and what should be unique
	// about them is their topology. So we generate IDs based on the topology of
	// the node. Taking the ids of all parents plus our own digest. IDs of
	// parents are also generated similarly, except for root nodes, where ID
	// will be the record digest. For example an ID could look like:
	// Digest("[[parent1.ID,parent2.ID],[parent3.ID]]::self.dgst")
	idBuilder := strings.Builder{}
	idBuilder.WriteByte('[')
	for i, inputs := range record.Inputs {
		if i > 0 {
			idBuilder.WriteByte(',')
		}
		idBuilder.WriteByte('[')
		for j, inp := range inputs {
			if j > 0 {
				idBuilder.WriteByte(',')
			}
			parent, err := marshalRecord(v1cfg, artifacts, inp.LinkIndex, cache)
			if err != nil {
				return nil, err
			}
			idBuilder.WriteString(parent.ID.String())
		}
		idBuilder.WriteString("]")
	}
	idBuilder.WriteString("]")
	if idBuilder.String() != "[]" {
		// not a root item.
		idBuilder.WriteString("::" + record.Digest.String())
		r.ID = digest.FromString(idBuilder.String())
	}

	for inputIdx, inputs := range record.Inputs {
		for _, inp := range inputs {
			parent, err := marshalRecord(v1cfg, artifacts, inp.LinkIndex, cache)
			if err != nil {
				return nil, err
			}
			// links:
			// `parent.ID/dgst/idx/selector/ID`, see
			// cache/remotecache/v1/parse.go#47
			filename := vertexPrefix(parent.ID, record.Digest, inputIdx, inp.Selector)
			filename += separator + r.ID.String() // actual compound ID
			artifacts[links+filename] = nil       // empty file

			// backlinks:
			// `ID/dgst/inputIdx/selector/parent.ID`
			filename = vertexPrefix(r.ID, record.Digest, inputIdx, inp.Selector)
			filename += separator + parent.ID.String() // actual compound ID of parent
			artifacts[backlinks+filename] = nil        // empty file
		}
	}

	var layers []v1.CacheLayer
	for _, res := range record.Results {
		if len(layers) > 0 {
			return nil, errors.WithStack(errors.New("TODO: handle more than one set of layers")) // never saw this, but better be loud about it.
		}
		visited := map[int]struct{}{}
		var err error
		layers, err = getLayersChain(v1cfg.Layers, res.LayerIndex, visited)
		if err != nil {
			return nil, err
		}
	}
	if len(record.ChainedResults) > 0 {
		return nil, errors.WithStack(errors.New("TODO: handle ChainedResults")) // never saw this, but better be loud about it.
	}

	artifacts[manifests+r.ID.String()] = MiniManifest{
		Layers: layers,
	}

	cache[idx] = r
	return r, nil
}

func getLayersChain(layers []v1.CacheLayer, idx int, visited map[int]struct{}) ([]v1.CacheLayer, error) {
	if _, ok := visited[idx]; ok {
		return nil, errors.Errorf("invalid looping layer")
	}
	visited[idx] = struct{}{}

	if idx < 0 || idx >= len(layers) {
		return nil, errors.Errorf("invalid layer index %d", idx)
	}

	l := layers[idx]
	if l.ParentIndex == -1 {
		return []v1.CacheLayer{l}, nil
	}

	ls, err := getLayersChain(layers, l.ParentIndex, visited)
	if err != nil {
		return nil, err
	}
	l.ParentIndex = len(ls) - 1
	return append(ls, l), nil
}

package registry

import (
	"context"
	"encoding/json"

	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
)

func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, _ map[string]string) (remotecache.Exporter, error) {
		return NewExporter(), nil
	}
}

func NewExporter() remotecache.Exporter {
	cc := v1.NewCacheChains()
	return &exporter{CacheExporterTarget: cc, chains: cc}
}

type exporter struct {
	solver.CacheExporterTarget
	chains *v1.CacheChains
}

func (ce *exporter) Finalize(ctx context.Context) (map[string]string, error) {
	return nil, nil
}

func (ce *exporter) ExportForLayers(layers []digest.Digest) ([]byte, error) {
	config, descs, err := ce.chains.Marshal()
	if err != nil {
		return nil, err
	}

	descs2 := map[digest.Digest]v1.DescriptorProviderPair{}
	for _, k := range layers {
		if v, ok := descs[k]; ok {
			descs2[k] = v
		}
	}

	cc := v1.NewCacheChains()
	if err := v1.ParseConfig(*config, descs, cc); err != nil {
		return nil, err
	}

	cfg, _, err := cc.Marshal()
	if err != nil {
		return nil, err
	}

	if len(cfg.Layers) == 0 {
		logrus.Warn("failed to match any cache with layers")
		return nil, nil
	}

	// TODO: are the layers always ordered already or should we check?

	dt, err := json.Marshal(cfg.Records)
	if err != nil {
		return nil, err
	}

	return dt, nil
}

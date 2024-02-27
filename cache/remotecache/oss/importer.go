package oss

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/labels"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// ResolveCacheImporterFunc for "OSS" cache importer.
func ResolveCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, errors.WithMessage(err, "failed to create azblob config")
		}

		ossClient, err := createOSSClient(config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, errors.WithMessage(err, "failed to create container client")
		}

		importer := &importer{
			config:    config,
			ossClient: ossClient,
		}

		return importer, ocispecs.Descriptor{}, nil
	}
}

var _ remotecache.Importer = &importer{}

type importer struct {
	config    *Config
	ossClient *ossClient
}

func (oi *importer) Resolve(ctx context.Context, _ ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	eg, ctx := errgroup.WithContext(ctx)
	ccs := make([]*v1.CacheChains, len(oi.config.Names))

	for i, name := range oi.config.Names {
		func(i int, name string) {
			eg.Go(func() error {
				cc, err := oi.loadManifest(ctx, name)
				if err != nil {
					return errors.Wrapf(err, "failed to load cache manifest %s", name)
				}
				ccs[i] = cc
				return nil
			})
		}(i, name)
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	cms := make([]solver.CacheManager, 0, len(ccs))

	for _, cc := range ccs {
		keysStorage, resultStorage, err := v1.NewCacheKeyStorage(cc, w)
		if err != nil {
			return nil, err
		}
		cms = append(cms, solver.NewCacheManager(ctx, id, keysStorage, resultStorage))
	}

	return solver.NewCombinedCacheManager(cms, nil), nil
}

func (oi *importer) loadManifest(ctx context.Context, name string) (*v1.CacheChains, error) {
	key := oi.ossClient.manifestKey(name)
	exists, err := blobExists(oi.ossClient, key)
	if err != nil {
		return nil, err
	}

	bklog.G(ctx).Debugf("name %s cache with key %s exists = %v", name, key, exists)

	if !exists {
		return v1.NewCacheChains(), nil
	}

	blobBody, err := oi.ossClient.Bucket.GetObject(key)
	if err != nil {
		return nil, errors.Wrap(err, "error get blob object")
	}
	defer blobBody.Close()

	bytes, err := io.ReadAll(blobBody)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	bklog.G(ctx).Debugf("imported config: %s", string(bytes))

	var config v1.CacheConfig
	if err := json.Unmarshal(bytes, &config); err != nil {
		return nil, errors.WithStack(err)
	}

	allLayers := v1.DescriptorProvider{}
	for _, l := range config.Layers {
		dpp, err := oi.makeDescriptorProviderPair(l)
		if err != nil {
			return nil, err
		}
		allLayers[l.Blob] = *dpp
	}

	progress.OneOff(ctx, fmt.Sprintf("found %d layers in cache", len(allLayers)))(nil)

	cc := v1.NewCacheChains()
	if err := v1.ParseConfig(config, allLayers, cc); err != nil {
		return nil, err
	}

	return cc, nil
}

func (oi *importer) makeDescriptorProviderPair(l v1.CacheLayer) (*v1.DescriptorProviderPair, error) {
	if l.Annotations == nil {
		return nil, errors.Errorf("cache layer with missing annotations")
	}
	annotations := map[string]string{}
	if l.Annotations.DiffID == "" {
		return nil, errors.Errorf("cache layer with missing diffid")
	}
	annotations[labels.LabelUncompressed] = l.Annotations.DiffID.String()
	if !l.Annotations.CreatedAt.IsZero() {
		txt, err := l.Annotations.CreatedAt.MarshalText()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		annotations["buildkit/createdat"] = string(txt)
	}
	desc := ocispecs.Descriptor{
		MediaType:   l.Annotations.MediaType,
		Digest:      l.Blob,
		Size:        l.Annotations.Size,
		Annotations: annotations,
	}
	return &v1.DescriptorProviderPair{
		Descriptor: desc,
		Provider: &ossProvider{
			desc:      desc,
			ossClient: oi.ossClient,
			Provider:  contentutil.FromFetcher(&fetcher{ossClient: oi.ossClient, config: oi.config}),
			config:    oi.config,
		},
	}, nil
}

type fetcher struct {
	ossClient *ossClient
	config    *Config
}

func (f *fetcher) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	key := f.ossClient.blobKey(desc.Digest)
	exists, err := blobExists(f.ossClient, key)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, errors.Errorf("blob %s not found", desc.Digest)
	}

	bklog.G(ctx).Debugf("reading layer from cache: %s", key)

	blobBody, err := f.ossClient.Bucket.GetObject(key)
	if err != nil {
		return nil, errors.Wrap(err, "error get blob object")
	}
	return blobBody, nil
}

type ossProvider struct {
	content.Provider
	desc       ocispecs.Descriptor
	ossClient  *ossClient
	config     *Config
	checkMutex sync.Mutex
	checked    bool
}

func (p *ossProvider) CheckDescriptor(ctx context.Context, desc ocispecs.Descriptor) error {
	if desc.Digest != p.desc.Digest {
		return nil
	}

	if p.checked {
		return nil
	}

	p.checkMutex.Lock()
	defer p.checkMutex.Unlock()

	key := p.ossClient.blobKey(desc.Digest)
	exists, err := blobExists(p.ossClient, key)
	if err != nil {
		return err
	}

	if !exists {
		return errors.Errorf("blob %s not found", desc.Digest)
	}

	p.checked = true
	return nil
}

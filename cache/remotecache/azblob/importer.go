package azblob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// ResolveCacheImporterFunc for "azblob" cache importer.
func ResolveCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, fmt.Errorf("failed to create azblob config: %v", err)
		}

		containerClient, err := createContainerClient(ctx, config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, fmt.Errorf("failed to create container client: %v", err)
		}

		importer := &importer{
			config:          config,
			containerClient: containerClient,
		}

		return importer, ocispecs.Descriptor{}, nil
	}
}

var _ remotecache.Importer = &importer{}

type importer struct {
	config          *Config
	containerClient *azblob.ContainerClient
}

func (ci *importer) Resolve(ctx context.Context, _ ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	eg, ctx := errgroup.WithContext(ctx)
	ccs := make([]*v1.CacheChains, len(ci.config.Names))

	for i, name := range ci.config.Names {
		func(i int, name string) {
			eg.Go(func() error {
				cc, err := ci.loadManifest(ctx, name)
				if err != nil {
					return fmt.Errorf("failed to load cache manifest %s: %v", name, err)
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

func (ci *importer) loadManifest(ctx context.Context, name string) (*v1.CacheChains, error) {
	key := manifestKey(ci.config, name)
	exists, err := blobExists(ctx, ci.containerClient, key)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("name %s cache with key %s exists = %v", name, key, exists)

	if !exists {
		return v1.NewCacheChains(), nil
	}

	blobClient, err := ci.containerClient.NewBlockBlobClient(key)
	if err != nil {
		return nil, errors.Wrap(err, "error creating container client")
	}

	res, err := blobClient.Download(ctx, &azblob.BlobDownloadOptions{})
	if err != nil {
		return nil, err
	}

	bytes, err := io.ReadAll(res.RawResponse.Body)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("imported config: %s", string(bytes))

	var config v1.CacheConfig
	if err := json.Unmarshal(bytes, &config); err != nil {
		return nil, errors.WithStack(err)
	}

	allLayers := v1.DescriptorProvider{}
	for _, l := range config.Layers {
		dpp, err := ci.makeDescriptorProviderPair(l)
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

func (ci *importer) makeDescriptorProviderPair(l v1.CacheLayer) (*v1.DescriptorProviderPair, error) {
	if l.Annotations == nil {
		return nil, errors.Errorf("cache layer with missing annotations")
	}
	annotations := map[string]string{}
	if l.Annotations.DiffID == "" {
		return nil, errors.Errorf("cache layer with missing diffid")
	}
	annotations["containerd.io/uncompressed"] = l.Annotations.DiffID.String()
	if !l.Annotations.CreatedAt.IsZero() {
		txt, err := l.Annotations.CreatedAt.MarshalText()
		if err != nil {
			return nil, err
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
		Provider: &ciProvider{
			desc:            desc,
			containerClient: ci.containerClient,
			Provider:        contentutil.FromFetcher(&fetcher{containerClient: ci.containerClient, config: ci.config}),
			config:          ci.config,
		},
	}, nil
}

type fetcher struct {
	containerClient *azblob.ContainerClient
	config          *Config
}

func (f *fetcher) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	key := blobKey(f.config, desc.Digest.String())
	exists, err := blobExists(ctx, f.containerClient, key)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, errors.Errorf("blob %s not found", desc.Digest)
	}

	logrus.Debugf("reading layer from cache: %s", key)

	blobClient, err := f.containerClient.NewBlockBlobClient(key)
	if err != nil {
		return nil, errors.Wrap(err, "error creating block blob client")
	}

	res, err := blobClient.Download(ctx, &azblob.BlobDownloadOptions{})
	if err != nil {
		return nil, err
	}

	return res.RawResponse.Body, nil
}

type ciProvider struct {
	content.Provider
	desc            ocispecs.Descriptor
	containerClient *azblob.ContainerClient
	config          *Config
	checkMutex      sync.Mutex
	checked         bool
}

func (p *ciProvider) CheckDescriptor(ctx context.Context, desc ocispecs.Descriptor) error {
	if desc.Digest != p.desc.Digest {
		return nil
	}

	if p.checked {
		return nil
	}

	p.checkMutex.Lock()
	defer p.checkMutex.Unlock()

	key := blobKey(p.config, desc.Digest.String())
	exists, err := blobExists(ctx, p.containerClient, key)
	if err != nil {
		return err
	}

	if !exists {
		return errors.Errorf("blob %s not found", desc.Digest)
	}

	p.checked = true
	return nil
}

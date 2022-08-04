package imageutil

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/resolver/limited"
	"github.com/moby/buildkit/util/resolver/retryhandler"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type ContentCache interface {
	content.Ingester
	content.Provider
}

var leasesMu sync.Mutex
var leasesF []func(context.Context) error

func CancelCacheLeases() {
	leasesMu.Lock()
	for _, f := range leasesF {
		f(context.TODO())
	}
	leasesF = nil
	leasesMu.Unlock()
}

func AddLease(f func(context.Context) error) {
	leasesMu.Lock()
	leasesF = append(leasesF, f)
	leasesMu.Unlock()
}

type ConfigResult struct {
	Digest   digest.Digest
	Config   []byte
	Manifest []byte
	Index    []byte
}

func Config(ctx context.Context, str string, resolver remotes.Resolver, cache ContentCache, leaseManager leases.Manager, p *ocispecs.Platform) (*ConfigResult, error) {
	// TODO: fix buildkit to take interface instead of struct
	var platform platforms.MatchComparer
	if p != nil {
		platform = platforms.Only(*p)
	} else {
		platform = platforms.Default()
	}
	ref, err := reference.Parse(str)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if leaseManager != nil {
		ctx2, done, err := leaseutil.WithLease(ctx, leaseManager, leases.WithExpiration(5*time.Minute), leaseutil.MakeTemporary)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		ctx = ctx2
		defer func() {
			// this lease is not deleted to allow other components to access manifest/config from cache. It will be deleted after 5 min deadline or on pruning inactive builder
			AddLease(done)
		}()
	}

	desc := ocispecs.Descriptor{
		Digest: ref.Digest(),
	}
	if desc.Digest != "" {
		ra, err := cache.ReaderAt(ctx, desc)
		if err == nil {
			desc.Size = ra.Size()
			mt, err := DetectManifestMediaType(ra)
			if err == nil {
				desc.MediaType = mt
			}
		}
	}
	// use resolver if desc is incomplete
	if desc.MediaType == "" {
		_, desc, err = resolver.Resolve(ctx, ref.String())
		if err != nil {
			return nil, err
		}
	}

	fetcher, err := resolver.Fetcher(ctx, ref.String())
	if err != nil {
		return nil, err
	}

	if desc.MediaType == images.MediaTypeDockerSchema1Manifest {
		return readSchema1Config(ctx, ref.String(), desc, fetcher, cache)
	}

	children := childrenConfigHandler(cache, platform)

	handlers := []images.Handler{
		retryhandler.New(limited.FetchHandler(cache, fetcher, str), func(_ []byte) {}),
		children,
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), nil, desc); err != nil {
		return nil, err
	}

	res := &ConfigResult{
		Digest: desc.Digest,
	}

	// NOTE: most of this logic is butchered from containerd's images.Manifest,
	// however, as it doesn't return a descriptor, we can't extract the raw
	// manifest content, so instead we Walk the image data manually.
	if err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispecs.MediaTypeImageManifest:
			manifestData, err := content.ReadBlob(ctx, cache, desc)
			if err != nil {
				return nil, err
			}
			var manifest ocispecs.Manifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				return nil, err
			}

			if desc.Digest != res.Digest && platform != nil {
				if desc.Platform != nil && !platform.Match(*desc.Platform) {
					return nil, nil
				}
			}

			configData, err := content.ReadBlob(ctx, cache, manifest.Config)
			if err != nil {
				return nil, err
			}
			var config ocispecs.Image
			if err := json.Unmarshal(configData, &config); err != nil {
				return nil, err
			}

			if desc.Digest != res.Digest && platform != nil {
				if !platform.Match(platforms.Normalize(ocispecs.Platform{OS: config.OS, Architecture: config.Architecture})) {
					return nil, nil
				}
			}

			res.Manifest = manifestData
			res.Config = configData

			return nil, nil
		case images.MediaTypeDockerSchema2ManifestList, ocispecs.MediaTypeImageIndex:
			indexData, err := content.ReadBlob(ctx, cache, desc)
			if err != nil {
				return nil, err
			}
			var idx ocispecs.Index
			if err := json.Unmarshal(indexData, &idx); err != nil {
				return nil, err
			}
			res.Index = indexData

			if platform == nil {
				return idx.Manifests, nil
			}

			var descs []ocispecs.Descriptor
			for _, d := range idx.Manifests {
				if d.Platform == nil || platform.Match(*d.Platform) {
					descs = append(descs, d)
				}
			}
			sort.SliceStable(descs, func(i, j int) bool {
				if descs[i].Platform == nil {
					return false
				}
				if descs[j].Platform == nil {
					return true
				}
				return platform.Less(*descs[i].Platform, *descs[j].Platform)
			})
			if len(descs) > 1 {
				return descs[:1], nil
			}
			return descs, nil
		}
		return nil, fmt.Errorf("unexpected media type %v for %v: %w", desc.MediaType, desc.Digest, errdefs.ErrNotFound)
	}), desc); err != nil {
		return nil, err
	}

	if res.Manifest == nil {
		err := fmt.Errorf("manifest %v: %w", res.Digest, errdefs.ErrNotFound)
		if res.Index != nil {
			err = fmt.Errorf("no match for platform in manifest %v: %w", res.Digest, errdefs.ErrNotFound)
		}
		return nil, err
	}

	return res, nil
}

func childrenConfigHandler(provider content.Provider, platform platforms.MatchComparer) images.HandlerFunc {
	return func(ctx context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		var descs []ocispecs.Descriptor
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispecs.MediaTypeImageManifest:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			// TODO(stevvooe): We just assume oci manifest, for now. There may be
			// subtle differences from the docker version.
			var manifest ocispecs.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			descs = append(descs, manifest.Config)
		case images.MediaTypeDockerSchema2ManifestList, ocispecs.MediaTypeImageIndex:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			var index ocispecs.Index
			if err := json.Unmarshal(p, &index); err != nil {
				return nil, err
			}

			if platform != nil {
				for _, d := range index.Manifests {
					if d.Platform == nil || platform.Match(*d.Platform) {
						descs = append(descs, d)
					}
				}
			} else {
				descs = append(descs, index.Manifests...)
			}
		case images.MediaTypeDockerSchema2Config, ocispecs.MediaTypeImageConfig, docker.LegacyConfigMediaType:
			// childless data types.
			return nil, nil
		default:
			return nil, errors.Errorf("encountered unknown type %v; children may not be fetched", desc.MediaType)
		}

		return descs, nil
	}
}

// specs.MediaTypeImageManifest, // TODO: detect schema1/manifest-list
func DetectManifestMediaType(ra content.ReaderAt) (string, error) {
	// TODO: schema1

	dt := make([]byte, ra.Size())
	if _, err := ra.ReadAt(dt, 0); err != nil {
		return "", err
	}

	return DetectManifestBlobMediaType(dt)
}

func DetectManifestBlobMediaType(dt []byte) (string, error) {
	var mfst struct {
		MediaType *string         `json:"mediaType"`
		Config    json.RawMessage `json:"config"`
		Manifests json.RawMessage `json:"manifests"`
		Layers    json.RawMessage `json:"layers"`
	}

	if err := json.Unmarshal(dt, &mfst); err != nil {
		return "", err
	}

	mt := images.MediaTypeDockerSchema2ManifestList

	if mfst.Config != nil || mfst.Layers != nil {
		mt = images.MediaTypeDockerSchema2Manifest

		if mfst.Manifests != nil {
			return "", errors.Errorf("invalid ambiguous manifest and manifest list")
		}
	}

	if mfst.MediaType != nil {
		switch *mfst.MediaType {
		case images.MediaTypeDockerSchema2ManifestList, ocispecs.MediaTypeImageIndex:
			if mt != images.MediaTypeDockerSchema2ManifestList {
				return "", errors.Errorf("mediaType in manifest does not match manifest contents")
			}
			mt = *mfst.MediaType
		case images.MediaTypeDockerSchema2Manifest, ocispecs.MediaTypeImageManifest:
			if mt != images.MediaTypeDockerSchema2Manifest {
				return "", errors.Errorf("mediaType in manifest does not match manifest contents")
			}
			mt = *mfst.MediaType
		}
	}
	return mt, nil
}

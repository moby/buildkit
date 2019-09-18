package imagerefchecker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/moby/buildkit/cache"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	emptyGZLayer = digest.Digest("sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1")
)

type Opt struct {
	ImageStore   images.Store
	ContentStore content.Store
}

// New creates new image reference checker that can be used to see if a reference
// is being used by any of the images in the image store
func New(opt Opt) cache.ExternalRefCheckerFunc {
	return func(cm cache.Accessor) (cache.ExternalRefChecker, error) {
		return &Checker{opt: opt, cm: cm}, nil
	}
}

type Checker struct {
	cm     cache.Accessor
	opt    Opt
	once   sync.Once
	images map[string]struct{}
	cache  map[string]bool
}

func (c *Checker) Exists(key string) bool {
	if c.opt.ImageStore == nil {
		return false
	}

	c.once.Do(c.init)

	if b, ok := c.cache[key]; ok {
		return b
	}

	l, err := c.getLayers(key)
	if err != nil {
		c.cache[key] = false
		return false
	}

	_, ok := c.images[layerKey(l)]
	c.cache[key] = ok
	return ok
}

func (c *Checker) getLayers(key string) ([]specs.Descriptor, error) {
	ref, err := c.cm.Get(context.TODO(), key)
	if err != nil {
		return nil, err
	}
	info := ref.Info()
	if info.Blob == "" {
		return nil, errors.Errorf("layer without blob")
	}
	var layers []specs.Descriptor
	if parent := ref.Parent(); parent != nil {
		layers, err = c.getLayers(parent.ID())
		if err != nil {
			return nil, err
		}
	}
	return append(layers, specs.Descriptor{Digest: info.Blob}), nil
}

func (c *Checker) init() {
	c.images = map[string]struct{}{}
	c.cache = map[string]bool{}

	imgs, err := c.opt.ImageStore.List(context.TODO())
	if err != nil {
		return
	}

	var mu sync.Mutex

	for _, img := range imgs {
		if err := images.Dispatch(context.TODO(), images.Handlers(layersHandler(c.opt.ContentStore, func(layers []specs.Descriptor) {
			mu.Lock()
			c.registerLayers(layers)
			mu.Unlock()
		})), nil, img.Target); err != nil {
			return
		}
	}
}

func (c *Checker) registerLayers(l []specs.Descriptor) {
	if k := layerKey(l); k != "" {
		c.images[k] = struct{}{}
	}
}

func layerKey(layers []specs.Descriptor) string {
	b := &strings.Builder{}
	for _, l := range layers {
		if l.Digest != emptyGZLayer {
			b.Write([]byte(l.Digest))
		}
	}
	return b.String()
}

func layersHandler(provider content.Provider, f func([]specs.Descriptor)) images.HandlerFunc {
	return func(ctx context.Context, desc specs.Descriptor) ([]specs.Descriptor, error) {
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, specs.MediaTypeImageManifest:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, nil
			}

			var manifest specs.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			f(manifest.Layers)
			return nil, nil
		case images.MediaTypeDockerSchema2ManifestList, specs.MediaTypeImageIndex:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, nil
			}

			var index specs.Index
			if err := json.Unmarshal(p, &index); err != nil {
				return nil, err
			}

			return index.Manifests, nil
		default:
			return nil, errors.Errorf("encountered unknown type %v", desc.MediaType)
		}
	}
}

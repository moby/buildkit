package push

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func Push(ctx context.Context, sm *session.Manager, sid string, cs content.Store, dgst digest.Digest, ref string, insecure bool, hosts docker.RegistryHosts, byDigest bool) error {
	desc := ocispec.Descriptor{
		Digest: dgst,
	}
	parsed, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return err
	}
	if byDigest && !reference.IsNameOnly(parsed) {
		return errors.Errorf("can't push tagged ref %s by digest", parsed.String())
	}

	if byDigest {
		ref = parsed.Name()
	} else {
		ref = reference.TagNameOnly(parsed).String()
	}

	resolver := resolver.New(hosts, sm, session.NewGroup(sid))

	pusher, err := resolver.Pusher(ctx, ref)
	if err != nil {
		return err
	}

	var m sync.Mutex
	manifestStack := []ocispec.Descriptor{}

	filterHandler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest,
			images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			m.Lock()
			manifestStack = append(manifestStack, desc)
			m.Unlock()
			return nil, images.ErrStopHandler
		default:
			return nil, nil
		}
	})

	pushHandler := remotes.PushHandler(pusher, cs)
	pushUpdateSourceHandler, err := updateDistributionSourceHandler(cs, pushHandler, ref)
	if err != nil {
		return err
	}

	handlers := append([]images.Handler{},
		images.HandlerFunc(annotateDistributionSourceHandler(cs, childrenHandler(cs))),
		filterHandler,
		dedupeHandler(pushUpdateSourceHandler),
	)

	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return err
	}

	mtype, err := imageutil.DetectManifestMediaType(ra)
	if err != nil {
		return err
	}

	layersDone := oneOffProgress(ctx, "pushing layers")
	err = images.Dispatch(ctx, images.Handlers(handlers...), nil, ocispec.Descriptor{
		Digest:    dgst,
		Size:      ra.Size(),
		MediaType: mtype,
	})
	layersDone(err)
	if err != nil {
		return err
	}

	mfstDone := oneOffProgress(ctx, fmt.Sprintf("pushing manifest for %s", ref))
	for i := len(manifestStack) - 1; i >= 0; i-- {
		_, err := pushHandler(ctx, manifestStack[i])
		if err != nil {
			mfstDone(err)
			return err
		}
	}
	mfstDone(nil)
	return nil
}

func annotateDistributionSourceHandler(cs content.Store, f images.HandlerFunc) func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		children, err := f(ctx, desc)
		if err != nil {
			return nil, err
		}

		// only add distribution source for the config or blob data descriptor
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest,
			images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
		default:
			return children, nil
		}

		for i := range children {
			child := children[i]

			info, err := cs.Info(ctx, child.Digest)
			if err != nil {
				return nil, err
			}

			for k, v := range info.Labels {
				if !strings.HasPrefix(k, "containerd.io/distribution.source.") {
					continue
				}

				if child.Annotations == nil {
					child.Annotations = map[string]string{}
				}
				child.Annotations[k] = v
			}

			children[i] = child
		}
		return children, nil
	}
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}

func childrenHandler(provider content.Provider) images.HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		var descs []ocispec.Descriptor
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			// TODO(stevvooe): We just assume oci manifest, for now. There may be
			// subtle differences from the docker version.
			var manifest ocispec.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			descs = append(descs, manifest.Config)
			descs = append(descs, manifest.Layers...)
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			p, err := content.ReadBlob(ctx, provider, desc)
			if err != nil {
				return nil, err
			}

			var index ocispec.Index
			if err := json.Unmarshal(p, &index); err != nil {
				return nil, err
			}

			for _, m := range index.Manifests {
				if m.Digest != "" {
					descs = append(descs, m)
				}
			}
		case images.MediaTypeDockerSchema2Layer, images.MediaTypeDockerSchema2LayerGzip,
			images.MediaTypeDockerSchema2Config, ocispec.MediaTypeImageConfig,
			ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip:
			// childless data types.
			return nil, nil
		default:
			logrus.Warnf("encountered unknown type %v; children may not be fetched", desc.MediaType)
		}

		return descs, nil
	}
}

// updateDistributionSourceHandler will update distribution source label after
// pushing layer successfully.
//
// FIXME(fuweid): There is race condition for current design of distribution
// source label if there are pull/push jobs consuming same layer.
func updateDistributionSourceHandler(cs content.Store, pushF images.HandlerFunc, ref string) (images.HandlerFunc, error) {
	updateF, err := docker.AppendDistributionSourceLabel(cs, ref)
	if err != nil {
		return nil, err
	}

	return images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		var islayer bool

		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Layer, images.MediaTypeDockerSchema2LayerGzip,
			ocispec.MediaTypeImageLayer, ocispec.MediaTypeImageLayerGzip:
			islayer = true
		}

		children, err := pushF(ctx, desc)
		if err != nil {
			return nil, err
		}

		// update distribution source to layer
		if islayer {
			if _, err := updateF(ctx, desc); err != nil {
				logrus.Warnf("failed to update distribution source for layer %v: %v", desc.Digest, err)
			}
		}
		return children, nil
	}), nil
}

func dedupeHandler(h images.HandlerFunc) images.HandlerFunc {
	var g flightcontrol.Group
	res := map[digest.Digest][]ocispec.Descriptor{}
	var mu sync.Mutex

	return images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		res, err := g.Do(ctx, desc.Digest.String(), func(ctx context.Context) (interface{}, error) {
			mu.Lock()
			if r, ok := res[desc.Digest]; ok {
				mu.Unlock()
				return r, nil
			}
			mu.Unlock()

			children, err := h(ctx, desc)
			if err != nil {
				return nil, err
			}

			mu.Lock()
			res[desc.Digest] = children
			mu.Unlock()
			return children, nil
		})
		if err != nil {
			return nil, err
		}
		if res == nil {
			return nil, nil
		}
		return res.([]ocispec.Descriptor), nil
	})
}

package cache

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/reference"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/leaseutil"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type Unlazier interface {
	Unlazy(ctx context.Context) error
}

func (sr *immutableRef) GetRemote(ctx context.Context, createIfNeeded bool, compressionType compression.Type) (*solver.Remote, error) {
	ctx, done, err := leaseutil.WithLease(ctx, sr.cm.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return nil, err
	}
	defer done(ctx)

	err = sr.computeBlobChain(ctx, createIfNeeded, compressionType)
	if err != nil {
		return nil, err
	}

	mprovider := lazyMultiProvider{mprovider: contentutil.NewMultiProvider(nil)}
	remote := &solver.Remote{
		Provider: mprovider,
	}

	for _, ref := range sr.parentRefChain() {
		desc, err := ref.ociDesc()
		if err != nil {
			return nil, err
		}

		// NOTE: The media type might be missing for some migrated ones
		// from before lease based storage. If so, we should detect
		// the media type from blob data.
		//
		// Discussion: https://github.com/moby/buildkit/pull/1277#discussion_r352795429
		if desc.MediaType == "" {
			desc.MediaType, err = compression.DetectLayerMediaType(ctx, sr.cm.ContentStore, desc.Digest, false)
			if err != nil {
				return nil, err
			}
		}

		dh := sr.descHandlers[desc.Digest]

		// update distribution source annotation
		if dh != nil && dh.ImageRef != "" {
			refspec, err := reference.Parse(dh.ImageRef)
			if err != nil {
				return nil, err
			}

			u, err := url.Parse("dummy://" + refspec.Locator)
			if err != nil {
				return nil, err
			}

			source, repo := u.Hostname(), strings.TrimPrefix(u.Path, "/")
			if desc.Annotations == nil {
				desc.Annotations = make(map[string]string)
			}
			dslKey := fmt.Sprintf("%s.%s", "containerd.io/distribution.source", source)
			desc.Annotations[dslKey] = repo
		}

		remote.Descriptors = append(remote.Descriptors, desc)
		mprovider.Add(lazyRefProvider{
			ref:  ref,
			desc: desc,
			dh:   sr.descHandlers[desc.Digest],
		})
	}
	return remote, nil
}

type lazyMultiProvider struct {
	mprovider *contentutil.MultiProvider
	plist     []lazyRefProvider
}

func (mp lazyMultiProvider) Add(p lazyRefProvider) {
	mp.mprovider.Add(p.desc.Digest, p)
	mp.plist = append(mp.plist, p)
}

func (mp lazyMultiProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	return mp.mprovider.ReaderAt(ctx, desc)
}

func (mp lazyMultiProvider) Unlazy(ctx context.Context) error {
	eg, egctx := errgroup.WithContext(ctx)
	for _, p := range mp.plist {
		eg.Go(func() error {
			return p.Unlazy(egctx)
		})
	}
	return eg.Wait()
}

type lazyRefProvider struct {
	ref  *immutableRef
	desc ocispec.Descriptor
	dh   *DescHandler
}

func (p lazyRefProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	if desc.Digest != p.desc.Digest {
		return nil, errdefs.ErrNotFound
	}
	if err := p.Unlazy(ctx); err != nil {
		return nil, err
	}
	return p.ref.cm.ContentStore.ReaderAt(ctx, desc)
}

func (p lazyRefProvider) Unlazy(ctx context.Context) error {
	_, err := p.ref.cm.unlazyG.Do(ctx, string(p.desc.Digest), func(ctx context.Context) (_ interface{}, rerr error) {
		if isLazy, err := p.ref.isLazy(ctx); err != nil {
			return nil, err
		} else if !isLazy {
			return nil, nil
		}

		if p.dh == nil {
			// shouldn't happen, if you have a lazy immutable ref it already should be validated
			// that descriptor handlers exist for it
			return nil, errors.New("unexpected nil descriptor handler")
		}

		if p.dh.Progress != nil {
			var stopProgress func(error)
			ctx, stopProgress = p.dh.Progress.Start(ctx)
			defer stopProgress(rerr)
		}

		// For now, just pull down the whole content and then return a ReaderAt from the local content
		// store. If efficient partial reads are desired in the future, something more like a "tee"
		// that caches remote partial reads to a local store may need to replace this.
		err := contentutil.Copy(ctx, p.ref.cm.ContentStore, &providerWithProgress{
			provider: p.dh.Provider,
			manager:  p.ref.cm.ContentStore,
		}, p.desc)
		if err != nil {
			return nil, err
		}

		if p.dh.ImageRef != "" {
			if GetDescription(p.ref.md) == "" {
				queueDescription(p.ref.md, "pulled from "+p.dh.ImageRef)
				err := p.ref.md.Commit()
				if err != nil {
					return nil, err
				}
			}
		}
		return nil, err
	})
	return err
}

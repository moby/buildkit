package llbsolver

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images/converter"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/contentutil"
	bkconverter "github.com/moby/buildkit/util/converter"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const distributionSourceLabelPrefix = "containerd.io/distribution.source."

// timestampRewriter rewrites the layers of the remotes of a cache export so
// that the timestamps of their files are clamped to the epoch, as the
// rewrite-timestamp option of the image exporter does.
//
// Only the layers created locally are rewritten: layers that are not in the
// content store (lazy) or that were pulled from a registry (base images,
// imported cache) are kept as is, they are already published and rewriting
// them would require pulling them.
type timestampRewriter struct {
	epoch time.Time
	comp  compression.Config

	mu        sync.Mutex
	rewritten map[digest.Digest]*rewrittenDesc
}

type rewrittenDesc struct {
	once sync.Once
	desc *ocispecs.Descriptor // nil if unchanged
	err  error
}

func newTimestampRewriter(epoch time.Time, comp compression.Config) *timestampRewriter {
	return &timestampRewriter{
		epoch:     epoch,
		comp:      comp,
		rewritten: map[digest.Digest]*rewrittenDesc{},
	}
}

type resolveRemotesFunc = func(context.Context, solver.Result) ([]*solver.Remote, error)

func (r *timestampRewriter) wrap(resolve resolveRemotesFunc) resolveRemotesFunc {
	return func(ctx context.Context, res solver.Result) ([]*solver.Remote, error) {
		remotes, err := resolve(ctx, res)
		if err != nil {
			return nil, err
		}
		ref, ok := res.Sys().(*worker.WorkerRef)
		if !ok {
			return remotes, nil
		}
		cs := ref.Worker.ContentStore()
		out := make([]*solver.Remote, len(remotes))
		for i, remote := range remotes {
			if out[i], err = r.rewriteRemote(ctx, cs, remote); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
}

func (r *timestampRewriter) rewriteRemote(ctx context.Context, cs content.Store, remote *solver.Remote) (*solver.Remote, error) {
	store := contentutil.NewStoreWithProvider(cs, remote.Provider)
	descs := slices.Clone(remote.Descriptors)
	var changed bool
	for i, desc := range descs {
		newDesc, err := r.rewriteDesc(ctx, cs, store, desc)
		if err != nil {
			return nil, err
		}
		if newDesc != nil {
			descs[i] = *newDesc
			changed = true
		}
	}
	if !changed {
		return remote, nil
	}
	var provider content.InfoReaderProvider = store
	if unlazier, ok := remote.Provider.(cache.Unlazier); ok {
		// the layers that were not rewritten may still be lazy
		provider = &unlazyStore{Store: store, unlazier: unlazier}
	}
	return &solver.Remote{Descriptors: descs, Provider: provider}, nil
}

func (r *timestampRewriter) rewriteDesc(ctx context.Context, cs, store content.Store, desc ocispecs.Descriptor) (*ocispecs.Descriptor, error) {
	r.mu.Lock()
	rw, ok := r.rewritten[desc.Digest]
	if !ok {
		rw = &rewrittenDesc{}
		r.rewritten[desc.Digest] = rw
	}
	r.mu.Unlock()
	rw.once.Do(func() {
		rw.desc, rw.err = r.doRewriteDesc(ctx, cs, store, desc)
	})
	return rw.desc, rw.err
}

func (r *timestampRewriter) doRewriteDesc(ctx context.Context, cs, store content.Store, desc ocispecs.Descriptor) (*ocispecs.Descriptor, error) {
	info, err := cs.Info(ctx, desc.Digest)
	if err != nil {
		// lazy blob
		return nil, nil
	}
	if hasDistributionSource(info.Labels) || hasDistributionSource(desc.Annotations) {
		return nil, nil
	}

	comp := r.comp
	if !comp.Force {
		// keep the compression of the layer
		t, err := compression.FromMediaType(desc.MediaType)
		if err != nil {
			return nil, nil
		}
		comp.Type = t
	}
	var fn converter.ConvertFunc
	fn, err = bkconverter.NewWithRewriteTimestamp(ctx, store, desc, comp, &r.epoch, nil)
	if err != nil || fn == nil {
		return nil, err
	}
	newDesc, err := fn(ctx, store, desc)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("failed to rewrite cache layer %s to match source-date-epoch %d", desc.Digest, r.epoch.Unix())
		return nil, nil
	}
	// nothing to clamp: keep the original blob, so the layer can still be
	// matched with the local one
	if newDesc == nil || (newDesc.MediaType == desc.MediaType && desc.Annotations[labels.LabelUncompressed] != "" &&
		newDesc.Annotations[labels.LabelUncompressed] == desc.Annotations[labels.LabelUncompressed]) {
		return nil, nil
	}
	return newDesc, nil
}

func hasDistributionSource(m map[string]string) bool {
	for k := range m {
		if strings.HasPrefix(k, distributionSourceLabelPrefix) {
			return true
		}
	}
	return false
}

type unlazyStore struct {
	content.Store
	unlazier cache.Unlazier
}

func (p *unlazyStore) Unlazy(ctx context.Context) error {
	return p.unlazier.Unlazy(ctx)
}

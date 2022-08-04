package imagemetaresolver

import (
	"context"
	"net/http"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/version"
	"github.com/moby/locker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

var defaultImageMetaResolver llb.ImageMetaResolver
var defaultImageMetaResolverOnce sync.Once

var WithDefault = imageOptionFunc(func(ii *llb.ImageInfo) {
	llb.WithMetaResolver(Default()).SetImageOption(ii)
})

type imageMetaResolverOpts struct {
	platform *ocispecs.Platform
}

type ImageMetaResolverOpt func(o *imageMetaResolverOpts)

func WithDefaultPlatform(p *ocispecs.Platform) ImageMetaResolverOpt {
	return func(o *imageMetaResolverOpts) {
		o.platform = p
	}
}

func New(with ...ImageMetaResolverOpt) llb.ImageMetaResolver {
	var opts imageMetaResolverOpts
	for _, f := range with {
		f(&opts)
	}
	headers := http.Header{}
	headers.Set("User-Agent", version.UserAgent())
	return &imageMetaResolver{
		resolver: docker.NewResolver(docker.ResolverOptions{
			Client:  http.DefaultClient,
			Headers: headers,
		}),
		platform: opts.platform,
		buffer:   contentutil.NewBuffer(),
		cache:    map[string]llb.ResolveImageConfigResult{},
		locker:   locker.New(),
	}
}

func Default() llb.ImageMetaResolver {
	defaultImageMetaResolverOnce.Do(func() {
		defaultImageMetaResolver = New()
	})
	return defaultImageMetaResolver
}

type imageMetaResolver struct {
	resolver remotes.Resolver
	buffer   contentutil.Buffer
	platform *ocispecs.Platform
	locker   *locker.Locker
	cache    map[string]llb.ResolveImageConfigResult
}

func (imr *imageMetaResolver) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (llb.ResolveImageConfigResult, error) {
	imr.locker.Lock(ref)
	defer imr.locker.Unlock(ref)

	platform := opt.Platform
	if platform == nil {
		platform = imr.platform
	}

	k := imr.key(ref, platform)

	if res, ok := imr.cache[k]; ok {
		return res, nil
	}

	dgst, config, err := imageutil.Config(ctx, ref, imr.resolver, imr.buffer, nil, platform)
	if err != nil {
		return llb.ResolveImageConfigResult{}, err
	}

	res := llb.ResolveImageConfigResult{Digest: dgst, Config: config}
	imr.cache[k] = res
	return res, nil
}

func (imr *imageMetaResolver) key(ref string, platform *ocispecs.Platform) string {
	if platform != nil {
		ref += platforms.Format(*platform)
	}
	return ref
}

type imageOptionFunc func(*llb.ImageInfo)

func (fn imageOptionFunc) SetImageOption(ii *llb.ImageInfo) {
	fn(ii)
}

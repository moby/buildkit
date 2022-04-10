package pin

import (
	"context"
	"fmt"
	"sync"

	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	pintypes "github.com/moby/buildkit/util/pin/types"
	"github.com/moby/buildkit/util/urlutil"
	digest "github.com/opencontainers/go-digest"
)

// Applier applies the pins.
// Appliers implements the llb.ImageMetaResolver interface.
type Applier struct {
	Pin               pintypes.Pin
	ImageMetaResolver llb.ImageMetaResolver
	consumed          pintypes.Consumed
	consumedMu        sync.Mutex
}

// ResolveImageConfig resolves "docker-image" sources.
// ImageMetaResolver falls back to a.ImageMetaResolver when the ref is not present in a.Pin .
// ResolveImageConfig implements the llb.ImageMetaResolver interface.
func (a *Applier) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (digest.Digest, []byte, error) {
	refParsed, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", nil, err
	}
	refQuery := reference.TagNameOnly(refParsed).String()

	var (
		hit       *binfotypes.Source
		pinnedRef string
	)
	for _, f := range a.Pin.Sources {
		f := f
		if f.Type != binfotypes.SourceTypeDockerImage {
			continue
		}
		if f.Ref != refQuery {
			continue
		}
		fRefParsed, err := reference.ParseNormalizedNamed(f.Ref)
		if err != nil {
			return "", nil, err
		}
		fDigest, err := digest.Parse(f.Pin)
		if err != nil {
			return "", nil, err
		}
		pinned, err := reference.WithDigest(fRefParsed, fDigest)
		if err != nil {
			return "", nil, err
		}
		if hit != nil {
			return "", nil, fmt.Errorf("found multiple pin entries for %q", refQuery)
		}
		hit = &f
		pinnedRef = pinned.String()
	}
	if hit != nil {
		a.consumedMu.Lock()
		a.consumed.Sources = append(a.consumed.Sources, *hit)
		a.consumedMu.Unlock()
	}
	if pinnedRef != "" {
		ref = pinnedRef
	}
	return a.ImageMetaResolver.ResolveImageConfig(ctx, ref, opt)
}

// HTTPChecksum returns the checksum if url is present in a.Pin.
// Otherwise returns an empty string without an error.
func (a *Applier) HTTPChecksum(url string) (digest.Digest, error) {
	urlQuery := urlutil.RedactCredentials(url)
	var (
		hit *binfotypes.Source
		d   digest.Digest
	)
	for _, f := range a.Pin.Sources {
		f := f
		if f.Type != binfotypes.SourceTypeHTTP {
			continue
		}
		if f.Ref != urlQuery {
			continue
		}
		if hit != nil {
			return "", fmt.Errorf("found multiple pin entries for %q", urlQuery)
		}
		hit = &f
		var err error
		d, err = digest.Parse(f.Pin)
		if err != nil {
			return d, err
		}
	}
	if hit != nil {
		a.consumedMu.Lock()
		a.consumed.Sources = append(a.consumed.Sources, *hit)
		a.consumedMu.Unlock()
	}
	return d, nil
}

func (a *Applier) Consumed() pintypes.Consumed {
	a.consumedMu.Lock()
	defer a.consumedMu.Unlock()
	return a.consumed
}

var _ llb.ImageMetaResolver = &Applier{}

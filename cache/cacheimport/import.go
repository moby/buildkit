package cacheimport

import (
	"context"
	"encoding/json"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/rootfs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/blobs"
	"github.com/moby/buildkit/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type ImportOpt struct {
	ContentStore  content.Store
	Snapshotter   snapshot.Snapshotter
	Applier       rootfs.Applier
	CacheAccessor cache.Accessor
}

func NewCacheImporter(opt ImportOpt) *CacheImporter {
	return &CacheImporter{opt: opt}
}

type CacheImporter struct {
	opt ImportOpt
}

func (ci *CacheImporter) Import(ctx context.Context, dgst digest.Digest) (InstructionCache, error) {
	dt, err := content.ReadBlob(ctx, ci.opt.ContentStore, dgst)
	if err != nil {
		return nil, err
	}

	var mfst ocispec.Index
	if err := json.Unmarshal(dt, &mfst); err != nil {
		return nil, err
	}

	allBlobs := map[digest.Digest]ocispec.Descriptor{}
	byCacheKey := map[digest.Digest]ocispec.Descriptor{}
	byContentKey := map[digest.Digest][]digest.Digest{}

	for _, m := range mfst.Manifests {
		if m.Digest != "" {
			allBlobs[m.Digest] = m
		}
		if m.Annotations != nil {
			if cacheKey := m.Annotations["buildkit.cachekey"]; cacheKey != "" {
				cacheKeyDigest, err := digest.Parse(cacheKey)
				if err != nil {
					return nil, err
				}
				byCacheKey[cacheKeyDigest] = m
				if contentKey := m.Annotations["buildkit.contentkey"]; contentKey != "" {
					contentKeyDigest, err := digest.Parse(contentKey)
					if err != nil {
						return nil, err
					}
					byContentKey[contentKeyDigest] = append(byContentKey[contentKeyDigest], cacheKeyDigest)
				}
			}
		}
	}

	return &importInfo{
		CacheImporter: ci,
		byCacheKey:    byCacheKey,
		byContentKey:  byContentKey,
		allBlobs:      allBlobs,
	}, nil
}

type importInfo struct {
	*CacheImporter
	byCacheKey   map[digest.Digest]ocispec.Descriptor
	byContentKey map[digest.Digest][]digest.Digest
	allBlobs     map[digest.Digest]ocispec.Descriptor
}

func (ii *importInfo) Probe(ctx context.Context, key digest.Digest) (bool, error) {
	_, ok := ii.byCacheKey[key]
	return ok, nil
}

func (ii *importInfo) getChain(dgst digest.Digest) ([]blobs.DiffPair, error) {
	desc, ok := ii.allBlobs[dgst]
	if !ok {
		return nil, errors.Errorf("blob %s not found in cache", dgst)
	}
	var parentDigest digest.Digest
	if desc.Annotations == nil {
		return nil, errors.Errorf("missing annotations")
	}
	parent := desc.Annotations["buildkit.parent"]
	if parent != "" {
		dgst, err := digest.Parse(parent)
		if err != nil {
			return nil, err
		}
		parentDigest = dgst
	}

	var out []blobs.DiffPair
	if parentDigest != "" {
		parentChain, err := ii.getChain(parentDigest)
		if err != nil {
			return nil, err
		}
		out = parentChain
	}

	diffIDStr := desc.Annotations["buildkit.diffid"]
	diffID, err := digest.Parse(diffIDStr)
	if err != nil {
		return nil, err
	}

	return append(out, blobs.DiffPair{Blobsum: dgst, DiffID: diffID}), nil
}

func (ii *importInfo) Lookup(ctx context.Context, key digest.Digest) (interface{}, error) {
	desc, ok := ii.byCacheKey[key]
	if !ok || desc.Digest == "" {
		return nil, nil
	}
	ch, err := ii.getChain(desc.Digest)
	if err != nil {
		return nil, err
	}
	return ii.fetch(ctx, ch)
}

func (ii *importInfo) Set(key digest.Digest, ref interface{}) error {
	return nil
}

func (ii *importInfo) SetContentMapping(contentKey, key digest.Digest) error {
	return nil
}

func (ii *importInfo) GetContentMapping(dgst digest.Digest) ([]digest.Digest, error) {
	dgsts, ok := ii.byContentKey[dgst]
	if !ok {
		return nil, nil
	}
	return dgsts, nil
}

func (ii *importInfo) fetch(ctx context.Context, chain []blobs.DiffPair) (cache.ImmutableRef, error) {
	chainid, err := ii.unpack(ctx, chain)
	if err != nil {
		return nil, err
	}

	return ii.opt.CacheAccessor.Get(ctx, chainid, cache.WithDescription("imported cache")) // TODO: more descriptive name
}

func (ii *importInfo) unpack(ctx context.Context, dpairs []blobs.DiffPair) (string, error) {
	layers, err := ii.getLayers(ctx, dpairs)
	if err != nil {
		return "", err
	}

	chainID, err := rootfs.ApplyLayers(ctx, layers, ii.opt.Snapshotter, ii.opt.Applier)
	if err != nil {
		return "", err
	}

	if err := ii.fillBlobMapping(ctx, layers); err != nil {
		return "", err
	}

	return string(chainID), nil
}

func (ii *importInfo) fillBlobMapping(ctx context.Context, layers []rootfs.Layer) error {
	var chain []digest.Digest
	for _, l := range layers {
		chain = append(chain, l.Diff.Digest)
		chainID := identity.ChainID(chain)
		if err := ii.opt.Snapshotter.(blobmapper).SetBlob(ctx, string(chainID), l.Blob.Digest); err != nil {
			return err
		}
	}
	return nil
}

func (ii *importInfo) getLayers(ctx context.Context, dpairs []blobs.DiffPair) ([]rootfs.Layer, error) {
	layers := make([]rootfs.Layer, len(dpairs))
	for i := range dpairs {
		layers[i].Diff = ocispec.Descriptor{
			// TODO: derive media type from compressed type
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    dpairs[i].DiffID,
		}
		info, err := ii.opt.ContentStore.Info(ctx, dpairs[i].Blobsum)
		if err != nil {
			return nil, err
		}
		layers[i].Blob = ocispec.Descriptor{
			// TODO: derive media type from compressed type
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Digest:    dpairs[i].Blobsum,
			Size:      info.Size,
		}
	}
	return layers, nil
}

type InstructionCache interface {
	Probe(ctx context.Context, key digest.Digest) (bool, error)
	Lookup(ctx context.Context, key digest.Digest) (interface{}, error) // TODO: regular ref
	Set(key digest.Digest, ref interface{}) error
	SetContentMapping(contentKey, key digest.Digest) error
	GetContentMapping(dgst digest.Digest) ([]digest.Digest, error)
}

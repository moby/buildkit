package cacheimport

import (
	"bytes"
	gocontext "context"
	"encoding/json"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/rootfs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/blobs"
	"github.com/moby/buildkit/snapshot"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

type blobmapper interface {
	GetBlob(ctx gocontext.Context, key string) (digest.Digest, error)
	SetBlob(ctx gocontext.Context, key string, blob digest.Digest) error
}

type CacheRecord struct {
	CacheKey   digest.Digest
	Reference  cache.ImmutableRef
	ContentKey digest.Digest
}

type ExporterOpt struct {
	Snapshotter  snapshot.Snapshotter
	ContentStore content.Store
	Differ       rootfs.MountDiffer
}

func NewCacheExporter(opt ExporterOpt) *CacheExporter {
	return &CacheExporter{opt: opt}
}

type CacheExporter struct {
	opt ExporterOpt
}

func (ce *CacheExporter) Export(ctx context.Context, rec []CacheRecord) error {
	allBlobs := map[digest.Digest][]blobs.DiffPair{}
	currentBlobs := map[digest.Digest]struct{}{}
	type cr struct {
		CacheRecord
		dgst digest.Digest
	}

	list := make([]cr, 0, len(rec))

	for _, r := range rec {
		ref := r.Reference
		if ref == nil {
			list = append(list, cr{CacheRecord: r})
			continue
		}

		dpairs, err := blobs.GetDiffPairs(ctx, ce.opt.Snapshotter, ce.opt.Differ, ref)
		if err != nil {
			return err
		}

		for i, dp := range dpairs {
			allBlobs[dp.Blobsum] = dpairs[:i+1]
		}

		dgst := dpairs[len(dpairs)-1].Blobsum
		list = append(list, cr{CacheRecord: r, dgst: dgst})
		currentBlobs[dgst] = struct{}{}
	}

	for b := range allBlobs {
		if _, ok := currentBlobs[b]; !ok {
			list = append(list, cr{dgst: b})
		}
	}

	var mfst ocispec.Index
	mfst.SchemaVersion = 2

	for _, l := range list {
		var size int64
		parent := ""
		diffID := ""
		if l.dgst != "" {
			info, err := ce.opt.ContentStore.Info(ctx, l.dgst)
			if err != nil {
				return err
			}
			size = info.Size
			chain := allBlobs[l.dgst]
			if len(chain) > 1 {
				parent = chain[len(chain)-2].Blobsum.String()
			}
			diffID = chain[len(chain)-1].DiffID.String()
		}

		mfst.Manifests = append(mfst.Manifests, ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayerGzip,
			Size:      size,
			Digest:    l.dgst,
			Annotations: map[string]string{
				"buildkit.cachekey":   l.CacheKey.String(),
				"buildkit.contentkey": l.ContentKey.String(),
				"buildkit.parent":     parent,
				"buildkit.diffid":     diffID,
			},
		})
	}

	dt, err := json.Marshal(mfst)
	if err != nil {
		return errors.Wrap(err, "failed to marshal manifest")
	}

	dgst := digest.FromBytes(dt)

	if err := content.WriteBlob(ctx, ce.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst); err != nil {
		return errors.Wrap(err, "error writing manifest blob")
	}

	logrus.Debugf("cache-manifest: %s", dgst)

	return nil
}

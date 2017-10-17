package blobs

import (
	gocontext "context"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/rootfs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/flightcontrol"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

var g flightcontrol.Group

type DiffPair struct {
	DiffID  digest.Digest
	Blobsum digest.Digest
}

type blobmapper interface {
	GetBlob(ctx gocontext.Context, key string) (digest.Digest, digest.Digest, error)
	SetBlob(ctx gocontext.Context, key string, diffID, blob digest.Digest) error
}

func GetDiffPairs(ctx context.Context, snapshotter snapshot.Snapshotter, differ rootfs.MountDiffer, ref cache.ImmutableRef) ([]DiffPair, error) {
	blobmap, ok := snapshotter.(blobmapper)
	if !ok {
		return nil, errors.Errorf("image exporter requires snapshotter with blobs mapping support")
	}

	eg, ctx := errgroup.WithContext(ctx)
	var diffPairs []DiffPair
	var currentPair DiffPair
	parent := ref.Parent()
	if parent != nil {
		defer parent.Release(context.TODO())
		eg.Go(func() error {
			dp, err := GetDiffPairs(ctx, snapshotter, differ, parent)
			if err != nil {
				return err
			}
			diffPairs = dp
			return nil
		})
	}
	eg.Go(func() error {
		dp, err := g.Do(ctx, ref.ID(), func(ctx context.Context) (interface{}, error) {
			diffID, blob, err := blobmap.GetBlob(ctx, ref.ID())
			if err != nil {
				return nil, err
			}
			if blob != "" {
				return DiffPair{DiffID: diffID, Blobsum: blob}, nil
			}
			// reference needs to be committed
			parent := ref.Parent()
			var lower []mount.Mount
			if parent != nil {
				defer parent.Release(context.TODO())
				lower, err = parent.Mount(ctx, true)
				if err != nil {
					return nil, err
				}
			}
			upper, err := ref.Mount(ctx, true)
			if err != nil {
				return nil, err
			}
			descr, err := differ.DiffMounts(ctx, lower, upper, ocispec.MediaTypeImageLayer, ref.ID())
			if err != nil {
				return nil, err
			}
			if err := blobmap.SetBlob(ctx, ref.ID(), descr.Digest, descr.Digest); err != nil {
				return nil, err
			}
			return DiffPair{DiffID: descr.Digest, Blobsum: descr.Digest}, nil
		})
		if err != nil {
			return err
		}
		currentPair = dp.(DiffPair)
		return nil
	})
	err := eg.Wait()
	if err != nil {
		return nil, err
	}
	return append(diffPairs, currentPair), nil
}

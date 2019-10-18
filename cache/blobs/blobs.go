package blobs

import (
	"context"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

var g flightcontrol.Group

const containerdUncompressed = "containerd.io/uncompressed"

type DiffPair struct {
	DiffID  digest.Digest
	Blobsum digest.Digest
}

var ErrNoBlobs = errors.Errorf("no blobs for snapshot")

// GetDiffPairs returns the DiffID/Blobsum pairs for a giver reference and saves it.
// Caller must hold a lease when calling this function.
func GetDiffPairs(ctx context.Context, contentStore content.Store, differ diff.Comparer, ref cache.ImmutableRef, createBlobs bool) ([]DiffPair, error) {
	if ref == nil {
		return nil, nil
	}

	if _, ok := leases.FromContext(ctx); !ok {
		return nil, errors.Errorf("missing lease requirement for GetDiffPairs")
	}

	if err := ref.Finalize(ctx, true); err != nil {
		return nil, err
	}

	if isTypeWindows(ref) {
		ctx = winlayers.UseWindowsLayerMode(ctx)
	}

	return getDiffPairs(ctx, contentStore, differ, ref, createBlobs)
}

func getDiffPairs(ctx context.Context, contentStore content.Store, differ diff.Comparer, ref cache.ImmutableRef, createBlobs bool) ([]DiffPair, error) {
	if ref == nil {
		return nil, nil
	}

	baseCtx := ctx
	eg, ctx := errgroup.WithContext(ctx)
	var diffPairs []DiffPair
	var currentDescr ocispec.Descriptor
	parent := ref.Parent()
	if parent != nil {
		defer parent.Release(context.TODO())
		eg.Go(func() error {
			dp, err := getDiffPairs(ctx, contentStore, differ, parent, createBlobs)
			if err != nil {
				return err
			}
			diffPairs = dp
			return nil
		})
	}
	eg.Go(func() error {
		dp, err := g.Do(ctx, ref.ID(), func(ctx context.Context) (interface{}, error) {
			refInfo := ref.Info()
			if refInfo.Blob != "" {
				return nil, nil
			} else if !createBlobs {
				return nil, errors.WithStack(ErrNoBlobs)
			}
			// reference needs to be committed
			parent := ref.Parent()
			var lower []mount.Mount
			var release func() error
			if parent != nil {
				defer parent.Release(context.TODO())
				m, err := parent.Mount(ctx, true)
				if err != nil {
					return nil, err
				}
				lower, release, err = m.Mount()
				if err != nil {
					return nil, err
				}
				if release != nil {
					defer release()
				}
			}
			m, err := ref.Mount(ctx, true)
			if err != nil {
				return nil, err
			}
			upper, release, err := m.Mount()
			if err != nil {
				return nil, err
			}
			if release != nil {
				defer release()
			}
			descr, err := differ.Compare(ctx, lower, upper,
				diff.WithMediaType(ocispec.MediaTypeImageLayerGzip),
				diff.WithReference(ref.ID()),
			)
			if err != nil {
				return nil, err
			}
			info, err := contentStore.Info(ctx, descr.Digest)
			if err != nil {
				return nil, err
			}
			if diffID, ok := info.Labels[containerdUncompressed]; !ok {
				return nil, errors.Errorf("invalid differ response with no diffID: %v", descr.Digest)
			} else {
				if descr.Annotations == nil {
					descr.Annotations = map[string]string{}
				}
				descr.Annotations[containerdUncompressed] = diffID
			}
			return descr, nil

		})
		if err != nil {
			return err
		}

		if dp != nil {
			currentDescr = dp.(ocispec.Descriptor)
		}
		return nil
	})
	err := eg.Wait()
	if err != nil {
		return nil, err
	}
	if currentDescr.Digest != "" {
		if err := ref.SetBlob(baseCtx, currentDescr); err != nil {
			return nil, err
		}
	}
	refInfo := ref.Info()
	return append(diffPairs, DiffPair{DiffID: refInfo.DiffID, Blobsum: refInfo.Blob}), nil
}

func isTypeWindows(ref cache.ImmutableRef) bool {
	if cache.GetLayerType(ref) == "windows" {
		return true
	}
	if parent := ref.Parent(); parent != nil {
		defer parent.Release(context.TODO())
		return isTypeWindows(parent)
	}
	return false
}

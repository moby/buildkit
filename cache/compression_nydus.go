//go:build nydus

package cache

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/nydus-snapshotter/pkg/converter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func init() {
	additionalAnnotations = append(
		additionalAnnotations,
		converter.LayerAnnotationNydusBlob, converter.LayerAnnotationNydusBootstrap,
	)
}

// MergeNydus does two steps:
// 1. Extracts nydus bootstrap from nydus format (nydus blob + nydus bootstrap) for each layer.
// 2. Merge all nydus bootstraps into a final bootstrap (will as an extra layer).
// The nydus bootstrap size is very small, so the merge operation is fast.
func MergeNydus(ctx context.Context, ref ImmutableRef, comp compression.Config, s session.Group) (*ocispecs.Descriptor, error) {
	iref, ok := ref.(*immutableRef)
	if !ok {
		return nil, fmt.Errorf("unsupported ref type %T", ref)
	}
	refs := iref.layerChain()
	if len(refs) == 0 {
		return nil, errors.New("refs can't be empty")
	}

	// Extracts nydus bootstrap from nydus format for each layer.
	var cm *cacheManager
	layers := []converter.Layer{}
	for _, ref := range refs {
		blobDesc, err := getBlobWithCompressionWithRetry(ctx, ref, comp, s)
		if err != nil {
			return nil, fmt.Errorf("get compression blob %q: %w", comp.Type, err)
		}
		ra, err := ref.cm.ContentStore.ReaderAt(ctx, blobDesc)
		if err != nil {
			return nil, fmt.Errorf("get reader for compression blob %q: %w", comp.Type, err)
		}
		defer ra.Close()
		if cm == nil {
			cm = ref.cm
		}
		layers = append(layers, converter.Layer{
			Digest:   blobDesc.Digest,
			ReaderAt: ra,
		})
	}

	// Merge all nydus bootstraps into a final nydus bootstrap.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		if _, err := converter.Merge(ctx, layers, pw, converter.MergeOption{
			WithTar: true,
		}); err != nil {
			pw.CloseWithError(fmt.Errorf("merge nydus bootstrap: %w", err))
		}
	}()

	// Compress final nydus bootstrap to tar.gz and write into content store.
	cw, err := content.OpenWriter(ctx, cm.ContentStore, content.WithRef("nydus-merge-"+iref.getChainID().String()))
	if err != nil {
		return nil, fmt.Errorf("open content store writer"+": %w", err)
	}
	defer cw.Close()

	gw := gzip.NewWriter(cw)
	uncompressedDgst := digest.SHA256.Digester()
	compressed := io.MultiWriter(gw, uncompressedDgst.Hash())
	if _, err := io.Copy(compressed, pr); err != nil {
		return nil, fmt.Errorf("copy bootstrap targz into content store: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer"+": %w", err)
	}

	compressedDgst := cw.Digest()
	if err := cw.Commit(ctx, 0, compressedDgst, content.WithLabels(map[string]string{
		labels.LabelUncompressed: uncompressedDgst.Digest().String(),
	})); err != nil {
		if !cerrdefs.IsAlreadyExists(err) {
			return nil, fmt.Errorf("commit to content store"+": %w", err)
		}
	}
	if err := cw.Close(); err != nil {
		return nil, fmt.Errorf("close content store writer"+": %w", err)
	}

	info, err := cm.ContentStore.Info(ctx, compressedDgst)
	if err != nil {
		return nil, fmt.Errorf("get info from content store"+": %w", err)
	}

	desc := ocispecs.Descriptor{
		Digest:    compressedDgst,
		Size:      info.Size,
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Annotations: map[string]string{
			labels.LabelUncompressed: uncompressedDgst.Digest().String(),
			// Use this annotation to identify nydus bootstrap layer.
			converter.LayerAnnotationNydusBootstrap: "true",
		},
	}

	return &desc, nil
}

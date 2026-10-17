//go:build nydus

package compression

import (
	"context"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	nydusify "github.com/containerd/nydus-snapshotter/pkg/converter"
)

type nydusType struct{}

var Nydus = nydusType{}

func init() {
	toDockerLayerType[nydusify.MediaTypeNydusBlob] = nydusify.MediaTypeNydusBlob
	toOCILayerType[nydusify.MediaTypeNydusBlob] = nydusify.MediaTypeNydusBlob
}

func Parse(t string) (Type, error) {
	ct, err := parse(t)
	if err != nil && t == Nydus.String() {
		return Nydus, nil
	}
	return ct, err
}

func FromMediaType(mediaType string) (Type, error) {
	ct, err := fromMediaType(mediaType)
	if err != nil && mediaType == nydusify.MediaTypeNydusBlob {
		return Nydus, nil
	}
	return ct, err
}

func (c nydusType) Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer) {
	digester := digest.Canonical.Digester()
	return func(dest io.Writer, requiredMediaType string) (io.WriteCloser, error) {
			writer := io.MultiWriter(dest, digester.Hash())
			return nydusify.Pack(ctx, writer, nydusify.PackOption{})
		}, func(ctx context.Context, cs content.Store) (map[string]string, error) {
			// Fill necessary labels
			uncompressedDgst := digester.Digest().String()
			info, err := cs.Info(ctx, digester.Digest())
			if err != nil {
				return nil, errors.Wrap(err, "get info from content store")
			}
			if info.Labels == nil {
				info.Labels = make(map[string]string)
			}
			info.Labels[labels.LabelUncompressed] = uncompressedDgst
			if _, err := cs.Update(ctx, info, "labels."+labels.LabelUncompressed); err != nil {
				return nil, errors.Wrap(err, "update info to content store")
			}

			// Fill annotations
			annotations := map[string]string{
				labels.LabelUncompressed: uncompressedDgst,
				// Use this annotation to identify nydus blob layer.
				nydusify.LayerAnnotationNydusBlob: "true",
			}
			return annotations, nil
		}
}

func (c nydusType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		if err := nydusify.Unpack(ctx, ra, pw, nydusify.UnpackOption{}); err != nil {
			pw.CloseWithError(errors.Wrap(err, "unpack nydus blob"))
		}
	}()

	return pr, nil
}

func (c nydusType) NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error) {
	if !images.IsLayerType(desc.MediaType) {
		return false, nil
	}
	_, hasAnno := desc.Annotations[nydusify.LayerAnnotationNydusBlob]
	return desc.MediaType != nydusify.MediaTypeNydusBlob || !hasAnno, nil
}

func (c nydusType) NeedsComputeDiffBySelf(comp Config) bool {
	return true
}

func (c nydusType) OnlySupportOCITypes() bool {
	return true
}

func (c nydusType) MediaType() string {
	return nydusify.MediaTypeNydusBlob
}

func (c nydusType) String() string {
	return "nydus"
}

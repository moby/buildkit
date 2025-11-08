//go:build nydus

package winlayers

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/archive"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	nydusify "github.com/containerd/nydus-snapshotter/pkg/converter"
)

func isNydusBlob(ctx context.Context, desc ocispecs.Descriptor) bool {
	if desc.Annotations == nil {
		return false
	}

	hasMediaType := desc.MediaType == nydusify.MediaTypeNydusBlob
	_, hasAnno := desc.Annotations[nydusify.LayerAnnotationNydusBlob]
	return hasMediaType && hasAnno
}

func (s *winApplier) apply(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, opts ...diff.ApplyOpt) (d ocispecs.Descriptor, err error) {
	if !isNydusBlob(ctx, desc) {
		return s.a.Apply(ctx, desc, mounts, opts...)
	}

	var ocidesc ocispecs.Descriptor
	if err := mount.WithTempMount(ctx, mounts, func(root string) error {
		ra, err := s.cs.ReaderAt(ctx, desc)
		if err != nil {
			return fmt.Errorf("get reader from content store"+": %w", err)
		}
		defer ra.Close()

		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			if err := nydusify.Unpack(ctx, ra, pw, nydusify.UnpackOption{}); err != nil {
				pw.CloseWithError(fmt.Errorf("unpack nydus blob"+": %w", err))
			}
		}()
		defer pr.Close()

		digester := digest.Canonical.Digester()
		rc := &readCounter{
			r: io.TeeReader(pr, digester.Hash()),
		}

		if _, err := archive.Apply(ctx, root, rc); err != nil {
			return fmt.Errorf("apply nydus blob"+": %w", err)
		}

		ocidesc = ocispecs.Descriptor{
			MediaType: ocispecs.MediaTypeImageLayer,
			Size:      rc.c,
			Digest:    digester.Digest(),
		}

		return nil
	}); err != nil {
		return ocispecs.Descriptor{}, err
	}

	return ocidesc, nil
}

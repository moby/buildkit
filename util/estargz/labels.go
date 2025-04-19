package estargz

import (
	"strings"

	"github.com/containerd/containerd/v2/pkg/labels"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// targetRefLabel is a label which contains image reference.
	//
	// It is a copy of [stargz-snapshotter/fs/source.targetRefLabel].
	//
	// [stargz-snapshotter/fs/source.targetRefLabel]: https://github.com/containerd/stargz-snapshotter/blob/v0.16.3/fs/source/source.go#L64-L65
	targetRefLabel = "containerd.io/snapshot/remote/stargz.reference"

	// targetDigestLabel is a label which contains layer digest.
	//
	// It is a copy of [stargz-snapshotter/fs/source.targetDigestLabel].
	//
	// [stargz-snapshotter/fs/source.targetDigestLabel]: https://github.com/containerd/stargz-snapshotter/blob/v0.16.3/fs/source/source.go#L67-L68
	targetDigestLabel = "containerd.io/snapshot/remote/stargz.digest"

	// targetImageLayersLabel is a label which contains layer digests contained in
	// the target image.
	//
	// It is a copy of [stargz-snapshotter/fs/source.targetImageLayersLabel].
	//
	// [stargz-snapshotter/fs/source.targetImageLayersLabel]: https://github.com/containerd/stargz-snapshotter/blob/v0.16.3/fs/source/source.go#L70-L72
	targetImageLayersLabel = "containerd.io/snapshot/remote/stargz.layers"
)

const (
	// TOCJSONDigestAnnotation is an annotation for an image layer. This stores the
	// digest of the TOC JSON.
	// This annotation is valid only when it is specified in `.[]layers.annotations`
	// of an image manifest.
	//
	// This is a copy of [estargz.TOCJSONDigestAnnotation]
	//
	// [estargz.TOCJSONDigestAnnotation]: https://pkg.go.dev/github.com/containerd/stargz-snapshotter/estargz@v0.16.3#TOCJSONDigestAnnotation
	TOCJSONDigestAnnotation = "containerd.io/snapshot/stargz/toc.digest"

	// StoreUncompressedSizeAnnotation is an additional annotation key for eStargz to enable lazy
	// pulling on containers/storage. Stargz Store is required to expose the layer's uncompressed size
	// to the runtime but current OCI image doesn't ship this information by default. So we store this
	// to the special annotation.
	//
	// This is a copy of [estargz.StoreUncompressedSizeAnnotation]
	//
	// [estargz.StoreUncompressedSizeAnnotation]: https://pkg.go.dev/github.com/containerd/stargz-snapshotter/estargz@v0.16.3#StoreUncompressedSizeAnnotation
	StoreUncompressedSizeAnnotation = "io.containers.estargz.uncompressed-size"
)

func SnapshotLabels(ref string, descs []ocispecs.Descriptor, targetIndex int) map[string]string {
	if len(descs) < targetIndex {
		return nil
	}
	desc := descs[targetIndex]

	var layers string
	for _, l := range descs[targetIndex:] {
		// This avoids the label hits the size limitation.
		// Skipping layers is allowed here and only affects performance.
		if err := labels.Validate(targetImageLayersLabel, layers+l.Digest.String()); err != nil {
			break
		}
		layers += l.Digest.String() + ","
	}
	return map[string]string{
		TOCJSONDigestAnnotation:         desc.Annotations[TOCJSONDigestAnnotation],
		StoreUncompressedSizeAnnotation: desc.Annotations[StoreUncompressedSizeAnnotation],
		targetRefLabel:                  ref,
		targetDigestLabel:               desc.Digest.String(),
		targetImageLayersLabel:          strings.TrimSuffix(layers, ","),
	}
}

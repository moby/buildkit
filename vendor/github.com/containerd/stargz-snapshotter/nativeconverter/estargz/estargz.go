/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package estargz

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/converter"
	"github.com/containerd/containerd/images/converter/uncompress"
	"github.com/containerd/containerd/labels"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// LayerConvertWithLayerOptsFunc converts legacy tar.gz layers into eStargz tar.gz layers.
// Media type is unchanged. Should be used in conjunction with WithDockerToOCI(). See
// LayerConvertFunc for more details. The difference between this function and
// LayerConvertFunc is that this allows to specify additional eStargz options per layer.
func LayerConvertWithLayerOptsFunc(opts map[digest.Digest][]estargz.Option) converter.ConvertFunc {
	if opts == nil {
		return LayerConvertFunc()
	}
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		// TODO: enable to speciy option per layer "index" because it's possible that there are
		//       two layers having same digest in an image (but this should be rare case)
		return LayerConvertFunc(opts[desc.Digest]...)(ctx, cs, desc)
	}
}

// LayerConvertFunc converts legacy tar.gz layers into eStargz tar.gz layers.
// Media type is unchanged.
//
// Should be used in conjunction with WithDockerToOCI().
//
// Otherwise "containerd.io/snapshot/stargz/toc.digest" annotation will be lost,
// because the Docker media type does not support layer annotations.
func LayerConvertFunc(opts ...estargz.Option) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispec.Descriptor) (*ocispec.Descriptor, error) {
		if !images.IsLayerType(desc.MediaType) {
			// No conversion. No need to return an error here.
			return nil, nil
		}
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, err
		}
		labelz := info.Labels
		if labelz == nil {
			labelz = make(map[string]string)
		}

		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, err
		}
		defer ra.Close()
		sr := io.NewSectionReader(ra, 0, desc.Size)
		blob, err := estargz.Build(sr, opts...)
		if err != nil {
			return nil, err
		}
		defer blob.Close()
		ref := fmt.Sprintf("convert-estargz-from-%s", desc.Digest)
		w, err := content.OpenWriter(ctx, cs, content.WithRef(ref))
		if err != nil {
			return nil, err
		}
		defer w.Close()

		// Reset the writing position
		// Old writer possibly remains without aborted
		// (e.g. conversion interrupted by a signal)
		if err := w.Truncate(0); err != nil {
			return nil, err
		}

		// Copy and count the contents
		pr, pw := io.Pipe()
		c := new(counter)
		doneCount := make(chan struct{})
		go func() {
			defer close(doneCount)
			defer pr.Close()
			decompressR, err := compression.DecompressStream(pr)
			if err != nil {
				pr.CloseWithError(err)
				return
			}
			defer decompressR.Close()
			if _, err := io.Copy(c, decompressR); err != nil {
				pr.CloseWithError(err)
				return
			}
		}()
		n, err := io.Copy(w, io.TeeReader(blob, pw))
		if err != nil {
			return nil, err
		}
		if err := blob.Close(); err != nil {
			return nil, err
		}
		if err := pw.Close(); err != nil {
			return nil, err
		}
		<-doneCount

		// update diffID label
		labelz[labels.LabelUncompressed] = blob.DiffID().String()
		if err = w.Commit(ctx, n, "", content.WithLabels(labelz)); err != nil && !errdefs.IsAlreadyExists(err) {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		newDesc := desc
		if uncompress.IsUncompressedType(newDesc.MediaType) {
			if images.IsDockerType(newDesc.MediaType) {
				newDesc.MediaType += ".gzip"
			} else {
				newDesc.MediaType += "+gzip"
			}
		}
		newDesc.Digest = w.Digest()
		newDesc.Size = n
		if newDesc.Annotations == nil {
			newDesc.Annotations = make(map[string]string, 1)
		}
		newDesc.Annotations[estargz.TOCJSONDigestAnnotation] = blob.TOCDigest().String()
		newDesc.Annotations[estargz.StoreUncompressedSizeAnnotation] = fmt.Sprintf("%d", c.size())
		return &newDesc, nil
	}
}

type counter struct {
	n  int64
	mu sync.Mutex
}

func (c *counter) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	c.n += int64(len(p))
	c.mu.Unlock()
	return len(p), nil
}

func (c *counter) size() (n int64) {
	c.mu.Lock()
	n = c.n
	c.mu.Unlock()
	return
}

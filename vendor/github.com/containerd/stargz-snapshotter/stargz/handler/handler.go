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

package handler

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/images"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	TargetRefLabel          = "containerd.io/snapshot/remote/stargz.reference"
	TargetDigestLabel       = "containerd.io/snapshot/remote/stargz.digest"
	TargetImageLayersLabel  = "containerd.io/snapshot/remote/stargz.layers"
	TargetPrefetchSizeLabel = "containerd.io/snapshot/remote/stargz.prefetch" // optional
)

// AppendInfoHandlerWrapper makes a handler which appends image's basic
// information to each layer descriptor as annotations during unpack. These
// annotations will be passed to this remote snapshotter as labels and used by
// this filesystem for searching/preparing layers.
func AppendInfoHandlerWrapper(ref string, prefetchSize int64) func(f images.Handler) images.Handler {
	return func(f images.Handler) images.Handler {
		return images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			children, err := f.Handle(ctx, desc)
			if err != nil {
				return nil, err
			}
			switch desc.MediaType {
			case ocispec.MediaTypeImageManifest, images.MediaTypeDockerSchema2Manifest:
				var layers string
				for _, c := range children {
					if images.IsLayerType(c.MediaType) {
						layers += fmt.Sprintf("%s,", c.Digest.String())
					}
				}
				if len(layers) >= 1 {
					layers = layers[:len(layers)-1]
				}
				for i := range children {
					c := &children[i]
					if images.IsLayerType(c.MediaType) {
						if c.Annotations == nil {
							c.Annotations = make(map[string]string)
						}
						c.Annotations[TargetRefLabel] = ref
						c.Annotations[TargetDigestLabel] = c.Digest.String()
						c.Annotations[TargetImageLayersLabel] = layers
						c.Annotations[TargetPrefetchSizeLabel] = fmt.Sprintf("%d", prefetchSize)
					}
				}
			}
			return children, nil
		})
	}
}

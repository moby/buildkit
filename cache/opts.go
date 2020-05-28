package cache

import (
	"fmt"

	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
)

type DescHandler struct {
	Provider content.Provider
	ImageRef string
	Progress progress.Controller
}

type DescHandlers map[digest.Digest]*DescHandler

func descHandlersOf(opts ...RefOption) DescHandlers {
	for _, opt := range opts {
		if opt, ok := opt.(DescHandlers); ok {
			return opt
		}
	}
	return nil
}

type DescHandlerKey digest.Digest

type NeedsRemoteProvidersError []digest.Digest

func (m NeedsRemoteProvidersError) Error() string {
	return fmt.Sprintf("missing descriptor handlers for lazy blobs %+v", []digest.Digest(m))
}

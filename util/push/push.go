package push

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func Push(ctx context.Context, cs content.Store, dgst digest.Digest, ref string) error {
	resolver := docker.NewResolver(docker.ResolverOptions{
		Client: http.DefaultClient,
	})

	pusher, err := resolver.Pusher(ctx, ref)
	if err != nil {
		return err
	}

	var m sync.Mutex
	manifestStack := []ocispec.Descriptor{}

	filterHandler := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest,
			images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			m.Lock()
			manifestStack = append(manifestStack, desc)
			m.Unlock()
			return nil, images.StopHandler
		default:
			return nil, nil
		}
	})

	pushHandler := remotes.PushHandler(cs, pusher)

	handlers := append([]images.Handler{},
		images.ChildrenHandler(cs),
		filterHandler,
		pushHandler,
	)

	info, err := cs.Info(ctx, dgst)
	if err != nil {
		return err
	}

	ra, err := cs.ReaderAt(ctx, dgst)
	if err != nil {
		return err
	}

	mtype, err := imageutil.DetectManifestMediaType(ra)
	if err != nil {
		return err
	}

	layersDone := oneOffProgress(ctx, "pushing layers")
	err = images.Dispatch(ctx, images.Handlers(handlers...), ocispec.Descriptor{
		Digest:    dgst,
		Size:      info.Size,
		MediaType: mtype,
	})
	layersDone(err)
	if err != nil {
		return err
	}

	mfstDone := oneOffProgress(ctx, fmt.Sprintf("pushing manifest for %s", ref))
	for i := len(manifestStack) - 1; i >= 0; i-- {
		_, err := pushHandler(ctx, manifestStack[i])
		if err != nil {
			mfstDone(err)
			return err
		}
	}
	mfstDone(nil)
	return nil
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}

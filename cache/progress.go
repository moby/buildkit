package cache

import (
	"context"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/remotes"
	"github.com/moby/buildkit/util/progress"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type providerWithProgress struct {
	provider content.Provider
	manager  interface {
		content.IngestManager
		content.Manager
	}
}

func (p *providerWithProgress) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	doneCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()

		pw, _, _ := progress.FromContext(ctx)
		defer pw.Close()

		ingestRef := remotes.MakeRefKey(ctx, desc)

		started := time.Now()
		onFinalStatus := false
		for {
			if onFinalStatus {
				return
			}
			select {
			case <-doneCh:
				onFinalStatus = true
			case <-ctx.Done():
				onFinalStatus = true
			case <-ticker.C:
			}

			status, err := p.manager.Status(ctx, ingestRef)
			if err == nil {
				pw.Write(desc.Digest.String(), progress.Status{
					Action:  "downloading",
					Current: int(status.Offset),
					Total:   int(status.Total),
					Started: &started,
				})
				continue
			} else if !errors.Is(err, errdefs.ErrNotFound) {
				logrus.Errorf("unexpected error getting ingest status of %q: %v", ingestRef, err)
				return
			}

			info, err := p.manager.Info(ctx, desc.Digest)
			if err == nil {
				pw.Write(desc.Digest.String(), progress.Status{
					Action:    "done",
					Current:   int(info.Size),
					Total:     int(info.Size),
					Started:   &started,
					Completed: &info.CreatedAt,
				})
				return
			}

			if errors.Is(err, errdefs.ErrNotFound) {
				pw.Write(desc.Digest.String(), progress.Status{
					Action: "waiting",
				})
			} else {
				logrus.Errorf("unexpected error getting content status of %q: %v", desc.Digest.String(), err)
				return
			}
		}
	}()

	ra, err := p.provider.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	return readerAtWithCloseCh{ReaderAt: ra, closeCh: doneCh}, nil
}

type readerAtWithCloseCh struct {
	content.ReaderAt
	closeCh chan struct{}
}

func (ra readerAtWithCloseCh) Close() error {
	close(ra.closeCh)
	return ra.ReaderAt.Close()
}

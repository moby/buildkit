package limited_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/resolver/limited"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestFetchHandlerWaitsBeforeOpeningWriter(t *testing.T) {
	for _, cancelWhileWaiting := range []bool{false, true} {
		name := "release"
		if cancelWhileWaiting {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				g := limited.New(1)
				data := "layer content"
				desc := ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayer,
					Digest:    digest.FromString(data),
					Size:      int64(len(data)),
				}
				fetcher := fetchFunc(func(context.Context, ocispecs.Descriptor) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader(data)), nil
				})
				// Occupy the same registry's slot using a different repository.
				rc, err := g.WrapFetcher(fetcher, "example.com/image").Fetch(ctx, desc)
				require.NoError(t, err)
				defer rc.Close()

				buffer := contentutil.NewBuffer()
				opened := false
				ingester := ingestFunc(func(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
					opened = true
					return buffer.Writer(ctx, opts...)
				})
				// A nested limited fetch must reuse the handler's acquired slot.
				h := g.FetchHandler(ingester, g.WrapFetcher(fetcher, "example.com/cache"), "example.com/cache")
				done := make(chan error, 1)
				go func() {
					_, err := h(ctx, desc)
					done <- err
				}()
				synctest.Wait()
				require.False(t, opened, "writer opened while the registry slot was occupied")

				if cancelWhileWaiting {
					cancel(context.Canceled)
					require.ErrorIs(t, <-done, context.Canceled)
					require.False(t, opened)
					require.NoError(t, rc.Close())
				} else {
					require.NoError(t, rc.Close())
					require.NoError(t, <-done)
					require.True(t, opened)
					got, err := content.ReadBlob(t.Context(), buffer, desc)
					require.NoError(t, err)
					require.Equal(t, data, string(got))
				}

				// Both completion and cancellation must leave the slot available.
				rc, err = g.WrapFetcher(fetcher, "example.com/other").Fetch(t.Context(), desc)
				require.NoError(t, err)
				require.NoError(t, rc.Close())
			})
		})
	}
}

func TestFetchHandlerReleasesSlotOnError(t *testing.T) {
	for _, stage := range []string{"writer", "fetch", "digest"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := limited.New(1)
				buffer := contentutil.NewBuffer()
				wantErr := errors.New("copy failed")
				desc := ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayer,
					Digest:    digest.FromString("good"),
					Size:      4,
				}
				ingester := ingestFunc(func(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
					if stage == "writer" {
						return nil, wantErr
					}
					return buffer.Writer(ctx, opts...)
				})
				fetcher := fetchFunc(func(context.Context, ocispecs.Descriptor) (io.ReadCloser, error) {
					if stage == "fetch" {
						return nil, wantErr
					}
					return io.NopCloser(strings.NewReader("bad!")), nil
				})
				_, err := g.FetchHandler(ingester, fetcher, "example.com/cache")(t.Context(), desc)
				if stage == "digest" {
					require.ErrorContains(t, err, "unexpected digest")
				} else {
					require.ErrorIs(t, err, wantErr)
				}
				_, err = buffer.Info(t.Context(), desc.Digest)
				require.Error(t, err, "failed content must not be committed")

				// A successful retry verifies that the registry slot was released.
				fetcher = fetchFunc(func(context.Context, ocispecs.Descriptor) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("good")), nil
				})
				_, err = g.FetchHandler(buffer, fetcher, "example.com/cache")(t.Context(), desc)
				require.NoError(t, err)
			})
		})
	}
}

type fetchFunc func(context.Context, ocispecs.Descriptor) (io.ReadCloser, error)

func (f fetchFunc) Fetch(ctx context.Context, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return f(ctx, desc)
}

type ingestFunc func(context.Context, ...content.WriterOpt) (content.Writer, error)

func (f ingestFunc) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return f(ctx, opts...)
}

package retryhandler

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd/images"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func New(f images.HandlerFunc) images.HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		backoff := time.Second
		var pw progress.Writer
		for {
			descs, err := f(ctx, desc)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil, err
				default:
					if !retryError(err) {
						return nil, err
					}
				}
				if pw == nil {
					pw, _, _ = progress.FromContext(ctx)
				}
				pw.Write(identity.NewID(), client.VertexLog{
					Stream: 2,
					Data:   []byte(fmt.Sprintf("error: %v\n", err.Error())),
				})
			} else {
				return descs, nil
			}
			// backoff logic
			if backoff >= 8*time.Second {
				return nil, err
			}
			pw.Write(identity.NewID(), client.VertexLog{
				Stream: 2,
				Data:   []byte(fmt.Sprintf("retrying in %v\n", backoff)),
			})
			time.Sleep(backoff)
			backoff *= 2
		}
	}
}

func retryError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// https://github.com/containerd/containerd/pull/4724
	if errors.Cause(err).Error() == "no response" {
		return true
	}

	// net.ErrClosed exposed in go1.16
	if strings.Contains(err.Error(), "use of closed network connection") {
		return true
	}

	return false
}

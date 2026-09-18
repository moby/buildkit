package retryhandler

import (
	"context"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// MaxRetryBackoff is the maximum backoff time before giving up. This is a
// variable so that code which embeds BuildKit can override the default value.
var MaxRetryBackoff = 8 * time.Second

func New(f images.HandlerFunc, logger func([]byte)) images.HandlerFunc {
	return func(ctx context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		var descs []ocispecs.Descriptor
		err := retry(ctx, logger, func() error {
			var err error
			descs, err = f(ctx, desc)
			return err
		})
		if err != nil {
			return nil, err
		}
		return descs, nil
	}
}

// Retry runs f again when it fails with the same transient errors handled by New.
func Retry(ctx context.Context, f func() error) error {
	return retry(ctx, nil, f)
}

func retry(ctx context.Context, logger func([]byte), f func() error) error {
	backoff := time.Second
	for {
		err := f()
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return err
		default:
			if !retryError(err) {
				return err
			}
		}

		if logger != nil {
			logger(fmt.Appendf(nil, "error: %v\n", err.Error()))
		}
		if backoff >= MaxRetryBackoff {
			return err
		}
		if logger != nil {
			logger(fmt.Appendf(nil, "retrying in %v\n", backoff))
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}

func retryError(err error) bool {
	// Retry on 5xx errors
	var errUnexpectedStatus remoteserrors.ErrUnexpectedStatus
	if errors.As(err, &errUnexpectedStatus) &&
		errUnexpectedStatus.StatusCode >= 500 &&
		errUnexpectedStatus.StatusCode <= 599 {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// catches TLS timeout or other network-related temporary errors
	if ne := net.Error(nil); errors.As(err, &ne) && ne.Temporary() { //nolint:staticcheck // ignoring "SA1019: Temporary is deprecated", continue to propagate net.Error through the "temporary" status
		return true
	}

	return false
}

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
		descs, err := WithRetry(ctx, logger, func(ctx context.Context) ([]ocispecs.Descriptor, error) {
			return f(ctx, desc)
		})
		if err != nil {
			return nil, err
		}
		return descs, nil
	}
}

type retryContextKey struct{}

// WithRetry retries transient failures with exponential backoff. Nested calls
// using the supplied context leave retries to the outer call, including New.
// f must be safe to retry and preserve retryable errors when wrapping them.
func WithRetry[T any](ctx context.Context, logger func([]byte), f func(context.Context) (T, error)) (T, error) {
	if ctx.Value(retryContextKey{}) != nil {
		return f(ctx)
	}
	ctx = context.WithValue(ctx, retryContextKey{}, true)
	backoff := time.Second
	for {
		v, err := f(ctx)
		if err == nil || context.Cause(ctx) != nil || !retryError(err) {
			return v, err
		}
		if logger != nil {
			logger(fmt.Appendf(nil, "error: %v\n", err.Error()))
		}
		if backoff >= MaxRetryBackoff {
			return v, err
		}
		if logger != nil {
			logger(fmt.Appendf(nil, "retrying in %v\n", backoff))
		}
		select {
		case <-ctx.Done():
			return v, err
		case <-time.After(backoff):
		}
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

	if errors.Is(err, io.EOF) || errors.Is(err, errConnectionReset) || errors.Is(err, syscall.EPIPE) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// catches TLS timeout or other network-related temporary errors
	if ne := net.Error(nil); errors.As(err, &ne) && ne.Temporary() { //nolint:staticcheck // ignoring "SA1019: Temporary is deprecated", continue to propagate net.Error through the "temporary" status
		return true
	}

	return false
}

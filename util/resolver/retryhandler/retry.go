package retryhandler

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/images"
	remoteserrors "github.com/containerd/containerd/remotes/errors"
	"github.com/docker/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/semaphore"
)

var mu sync.Mutex
var sem = map[string]*semaphore.Weighted{}

const connsPerHost = 4

func New(f images.HandlerFunc, ref string, logger func([]byte)) images.HandlerFunc {
	if ref != "" {
		if named, err := reference.ParseNormalizedNamed(ref); err == nil {
			ref = reference.Domain(named)
		}
	}

	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		mu.Lock()
		s, ok := sem[ref]
		if !ok {
			s = semaphore.NewWeighted(connsPerHost)
			sem[ref] = s
		}
		mu.Unlock()
		if err := s.Acquire(ctx, 1); err != nil {
			return nil, err
		}
		defer s.Release(1)

		backoff := time.Second
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
				if logger != nil {
					logger([]byte(fmt.Sprintf("error: %v\n", err.Error())))
				}
			} else {
				return descs, nil
			}
			// backoff logic
			if backoff >= 8*time.Second {
				return nil, err
			}
			if logger != nil {
				logger([]byte(fmt.Sprintf("retrying in %v\n", backoff)))
			}
			time.Sleep(backoff)
			backoff *= 2
		}
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
	if ne, ok := errors.Cause(err).(net.Error); ok && ne.Temporary() {
		return true
	}
	// https://github.com/containerd/containerd/pull/4724
	if errors.Cause(err).Error() == "no response" {
		return true
	}

	return false
}

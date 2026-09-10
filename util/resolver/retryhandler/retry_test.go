package retryhandler

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestRetryTransientError(t *testing.T) {
	var attempts atomic.Int32

	err := Retry(t.Context(), func() error {
		if attempts.Add(1) == 1 {
			return io.EOF
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, int32(2), attempts.Load())
}

func TestRetryPermanentError(t *testing.T) {
	var attempts atomic.Int32
	permanentErr := errors.New("permanent")

	err := Retry(t.Context(), func() error {
		attempts.Add(1)
		return permanentErr
	})

	require.ErrorIs(t, err, permanentErr)
	require.Equal(t, int32(1), attempts.Load())
}

func TestRetryStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var attempts atomic.Int32

	err := Retry(ctx, func() error {
		attempts.Add(1)
		cancel()
		return io.EOF
	})

	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, int32(1), attempts.Load())
}

func TestNewPreservesHandlerResults(t *testing.T) {
	expected := []ocispecs.Descriptor{{MediaType: "application/test"}}
	handler := New(func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		return expected, nil
	}, nil)

	got, err := handler(t.Context(), ocispecs.Descriptor{})
	require.NoError(t, err)
	require.Equal(t, expected, got)
}

func TestNewDiscardsResultsOnPermanentError(t *testing.T) {
	permanentErr := errors.New("permanent")
	handler := New(func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		return []ocispecs.Descriptor{{MediaType: "partial"}}, permanentErr
	}, nil)

	got, err := handler(t.Context(), ocispecs.Descriptor{})
	require.ErrorIs(t, err, permanentErr)
	require.Nil(t, got)
}

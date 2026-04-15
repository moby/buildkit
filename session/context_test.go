package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContextWithCaller(t *testing.T) {
	t.Run("caller close cancels derived context", func(t *testing.T) {
		callerCtx, callerCancel := context.WithCancelCause(context.Background())
		ctx := contextWithCaller(context.Background(), callerCtx)

		callerCancel(context.DeadlineExceeded)

		select {
		case <-ctx.Done():
			require.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for caller context cancellation")
		}
	})

	t.Run("request close cancels derived context", func(t *testing.T) {
		baseCtx, cancel := context.WithCancelCause(context.Background())
		ctx := contextWithCaller(baseCtx, context.Background())
		cancel(context.Canceled)

		select {
		case <-ctx.Done():
			require.ErrorIs(t, context.Cause(ctx), context.Canceled)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for request context cancellation")
		}
	})
}

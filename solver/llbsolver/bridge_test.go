package llbsolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLazyCacheManagerQueryContextCancellation(t *testing.T) {
	lcm := &lazyCacheManager{waitCh: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := lcm.QueryContext(ctx, nil, 0, "", 0)
		done <- err
	}()

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

package boltutil

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g gate
		g.enter()
		paused := make(chan bool, 1)
		go func() { paused <- g.pause(t.Context()) }()
		synctest.Wait()

		entered := make(chan struct{})
		go func() {
			g.enter()
			g.exit()
			close(entered)
		}()
		synctest.Wait()
		select {
		case <-entered:
			t.Fatal("new transaction entered during drain")
		default:
		}
		g.exit()
		require.True(t, <-paused)
		select {
		case <-entered:
			t.Fatal("new transaction entered during compaction")
		default:
		}
		g.unpause()
		<-entered
	})
}

func TestGateNestedTransaction(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var g gate
				g.enter()
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				ctx, cancelTimeout := context.WithTimeoutCause(ctx, time.Second, context.DeadlineExceeded)
				defer cancelTimeout()
				paused := make(chan bool, 1)
				go func() { paused <- g.pause(ctx) }()
				synctest.Wait()

				nested := make(chan struct{})
				go func() {
					g.enter()
					g.exit()
					close(nested)
				}()
				synctest.Wait()
				if !timeout {
					cancel(context.Canceled)
				}
				require.False(t, <-paused)
				<-nested
				g.exit()
				require.True(t, g.pause(t.Context()))
				g.unpause()
			})
		})
	}
}

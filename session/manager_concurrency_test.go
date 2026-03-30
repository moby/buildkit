package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
)

func TestManagerGetThunderingHerd(t *testing.T) {
	sm, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}

	// Create a dummy session to hit the "success" path of Get
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	if err := sm.withState(t.Context(), func(state *sessionState) error {
		state.active["test-session"] = &client{
			Session: Session{
				id:        "test-session",
				ctx:       ctx,
				cancelCtx: func(err error) { cancel(context.Canceled) },
				done:      make(chan struct{}),
			},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var ops int64

	start := time.Now()

	// 100 goroutines waiting for a non-existent session
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			waitCtx, waitCancel := context.WithTimeoutCause(context.Background(), 2*time.Second, errors.WithStack(context.DeadlineExceeded))
			defer waitCancel()

			// This will block in Wait() until timeout
			_, err := sm.Get(waitCtx, "non-existent", false)
			if err != nil {
				// expected to fail with timeout
				t.Logf("Waiter %d finished with error: %v", i, err)
			}
		}(i)
	}

	// 100 goroutines hammering Get for the existing session
	for range 100 {
		wg.Go(func() {
			for time.Since(start) < 2*time.Second {
				_, _ = sm.Get(context.Background(), "test-session", false)
				atomic.AddInt64(&ops, 1)
			}
		})
	}

	wg.Wait()
	t.Logf("Completed %d Get operations in 2 seconds", ops)

	if ops < 10000 {
		t.Errorf("Performance is abnormally low: only %d ops in 2 seconds", ops)
	}
}

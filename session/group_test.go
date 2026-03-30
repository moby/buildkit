package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAllSessionIDs(t *testing.T) {
	require.Nil(t, AllSessionIDs(nil))
	require.Equal(t, []string{"a", "b", "c"}, AllSessionIDs(NewGroup("a", "b", "c")))
}

func TestManagerAnyNilGroupIsNoop(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	err = sm.Any(t.Context(), nil, func(context.Context, string, Caller) error {
		return errors.New("callback should not execute for nil group")
	})
	require.NoError(t, err)
}

func TestManagerAnyReturnsLastErrorWhenContextCanceled(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)

	err = sm.Any(ctx, NewGroup("missing-a", "missing-b"), func(context.Context, string, Caller) error {
		return nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `session "missing-b" did not start`)
	require.Contains(t, err.Error(), "missing-b")
	require.Contains(t, err.Error(), "context canceled")
}

func TestManagerAnyCallbackErrorIsReturned(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const sessionID = "any-callback-session"
	_, cleanup := startSessionForTest(t.Context(), t, sm, sessionID, "any-key")
	defer cleanup()

	callbackErr := errors.New("callback failed")
	err = sm.Any(t.Context(), NewGroup(sessionID), func(_ context.Context, id string, c Caller) error {
		require.Equal(t, sessionID, id)
		require.NotNil(t, c)
		return callbackErr
	})
	require.ErrorContains(t, err, callbackErr.Error())
}

func TestManagerAnyReturnsFirstSuccessfulSession(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const sessionID = "any-success-session"
	_, cleanup := startSessionForTest(t.Context(), t, sm, sessionID, "any-success-key")
	defer cleanup()

	var called int32
	err = sm.Any(t.Context(), NewGroup(sessionID), func(_ context.Context, id string, c Caller) error {
		atomic.AddInt32(&called, 1)
		require.Equal(t, sessionID, id)
		require.NotNil(t, c)
		require.Equal(t, "any-success-key", c.SharedKey())
		return nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&called))
}

func TestManagerAnyHighConcurrencyContention(t *testing.T) {
	sm, err := NewManager()
	require.NoError(t, err)

	const sessionID = "any-contention"
	_, cleanup := startSessionForTest(t.Context(), t, sm, sessionID, "any-contention-key")
	defer cleanup()

	const workers = 250
	errCh := make(chan error, workers)

	for i := range workers {
		go func() {
			ctx, cancel := context.WithTimeoutCause(t.Context(), 3*time.Second, errors.WithStack(context.DeadlineExceeded))
			defer cancel()

			err := sm.Any(ctx, NewGroup(sessionID), func(cbCtx context.Context, id string, c Caller) error {
				if id != sessionID {
					return errors.Errorf("unexpected session id %q", id)
				}
				if c == nil {
					return errors.Errorf("nil caller")
				}
				if c.SharedKey() != "any-contention-key" {
					return errors.Errorf("unexpected shared key %q", c.SharedKey())
				}
				_, err := sm.Get(cbCtx, sessionID, i%2 == 0)
				return err
			})
			errCh <- err
		}()
	}

	for range workers {
		require.NoError(t, <-errCh)
	}
}

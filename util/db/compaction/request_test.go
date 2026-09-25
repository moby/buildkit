package compaction

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/stretchr/testify/require"
)

func TestManualRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		called := false
		b := &testBackend{size: 1000, free: 500, compact: func(context.Context) (db.CompactResult, error) {
			called = true
			return db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 500}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{}, b)
		require.NoError(t, err)
		defer s.Close()
		s.Begin(false)
		r, err := s.Request(t.Context())
		require.NoError(t, err)
		_, err = s.Request(t.Context())
		require.ErrorIs(t, err, ErrBusy)
		time.Sleep(2 * time.Second)
		require.False(t, called)
		s.End(false)
		time.Sleep(time.Second)
		synctest.Wait()
		require.True(t, (<-r.Done()).Result.Compacted)
		require.True(t, called)
	})
}

func TestManualRequestCancellation(t *testing.T) {
	for _, cause := range []string{"writer", "request", "shutdown"} {
		t.Run(cause, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				b := &testBackend{compact: func(ctx context.Context) (db.CompactResult, error) {
					close(started)
					<-ctx.Done()
					return db.CompactResult{}, context.Cause(ctx)
				}}
				cfg := testConfig()
				cfg.MaxRetry = 0
				s, err := New(t.Context(), cfg, State{}, b)
				require.NoError(t, err)
				defer s.Close()
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				r, err := s.Request(ctx)
				require.NoError(t, err)
				<-started
				switch cause {
				case "writer":
					s.Begin(true)
					s.End(true)
				case "request":
					cancel(context.Canceled)
				case "shutdown":
					s.Stop()
				}
				outcome := <-r.Done()
				require.False(t, outcome.Result.Compacted)
				require.NotEmpty(t, outcome.Error)
				synctest.Wait()
				s.mu.Lock()
				require.Zero(t, s.retries)
				require.False(t, s.manual)
				s.mu.Unlock()
			})
		})
	}
}

func TestManualRequestIdleCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{compact: func(context.Context) (db.CompactResult, error) {
			t.Fatal("must not copy while active")
			return db.CompactResult{}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{}, b)
		require.NoError(t, err)
		defer s.Close()
		s.Begin(false)
		ctx, cancel := context.WithCancelCause(t.Context())
		r, err := s.Request(ctx)
		require.NoError(t, err)
		cancel(context.Canceled)
		require.NotEmpty(t, (<-r.Done()).Error)
		s.End(false)
	})
}

func TestInspectDoesNotRequestCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 1000, free: 500}
		s, err := New(t.Context(), testConfig(), State{}, b)
		require.NoError(t, err)
		defer s.Close()
		status, err := s.Inspect()
		require.NoError(t, err)
		require.Equal(t, int64(500), status.Stats.Reclaimable)
		require.Zero(t, status.State.Writes)
		require.False(t, status.Pending)
		require.False(t, status.Manual)
	})
}

func TestManualRequestQueuedShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		s, err := New(ctx, testConfig(), State{}, &testBackend{})
		require.NoError(t, err)
		defer s.Close()
		r, err := s.Request(t.Context())
		require.NoError(t, err)
		cancel(context.Canceled)
		s.Stop()
		require.NotEmpty(t, (<-r.Done()).Error)
		_, err = s.Request(t.Context())
		require.Error(t, err)
	})
}

func TestManualOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		called := false
		b := &testBackend{size: 1000, free: 500, compact: func(context.Context) (db.CompactResult, error) {
			called = true
			return db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 500}, nil
		}}
		cfg := testConfig()
		cfg.ManualOnly = true
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		write(s)
		time.Sleep(2 * checkpointInterval)
		synctest.Wait()
		require.False(t, called)
		require.Zero(t, b.checks.Load())
		r, err := s.Request(t.Context())
		require.NoError(t, err)
		require.True(t, (<-r.Done()).Result.Compacted)
	})
}

func TestManualRequestSharesCheckpointSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{}
		cfg := testConfig()
		cfg.ManualOnly = true
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		s.Begin(false)
		defer s.End(false)
		time.Sleep(time.Minute)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		r, err := s.Request(ctx)
		require.NoError(t, err)
		time.Sleep(4 * time.Minute)
		synctest.Wait()
		require.Len(t, b.checkpoints(), 1)
		cancel(context.Canceled)
		require.NotEmpty(t, (<-r.Done()).Error)
		synctest.Wait()
		require.Len(t, b.checkpoints(), 1)
		time.Sleep(checkpointInterval)
		synctest.Wait()
		require.Len(t, b.checkpoints(), 2)
	})
}

package compaction

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/stretchr/testify/require"
)

type testBackend struct {
	mu      sync.Mutex
	size    int64
	free    int64
	checks  atomic.Int64
	saved   []State
	compact func(context.Context) (db.CompactResult, error)
}

func (b *testBackend) CompactionStats() (db.CompactionStats, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks.Add(1)
	return db.CompactionStats{Size: b.size, Reclaimable: b.free}, nil
}

func (b *testBackend) Save(s State) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saved = append(b.saved, s)
	return nil
}

func (b *testBackend) checkpoints() []State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]State(nil), b.saved...)
}

func (b *testBackend) Compact(ctx context.Context, _ db.CompactOptions) (db.CompactResult, error) {
	return b.compact(ctx)
}

func testConfig() Config {
	return Config{WriteWatermark: 2, MinReclaimBytes: 100, IdleTimeout: time.Second, MaxRetry: 2, MinReclaimPercent: 10}
}

func write(s *Scheduler) {
	s.Begin(true)
	s.End(true)
}

func TestTriggersAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		b := &testBackend{size: 100, free: 99, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		s.Begin(true)
		s.End(false)
		time.Sleep(checkpointInterval)
		synctest.Wait()
		require.Zero(t, calls.Load())
		write(s)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.Zero(t, calls.Load(), "reclaimable floor not reached")
		b.mu.Lock()
		b.free = 100
		b.mu.Unlock()
		s.Begin(false)
		time.Sleep(checkpointInterval)
		synctest.Wait()
		require.Zero(t, calls.Load(), "active reader prevents maintenance")
		s.End(false)
		time.Sleep(500 * time.Millisecond)
		s.Begin(false)
		s.End(false)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		require.Zero(t, calls.Load(), "reader resets idle timeout")
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		require.Equal(t, uint64(0), s.state.Writes)
		require.Equal(t, uint64(2), s.state.WriteWatermark)
	})
}

func TestPeriodicReclaimability(t *testing.T) {
	for _, manualOnly := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int64
			b := &testBackend{size: 1000, free: 99, compact: func(context.Context) (db.CompactResult, error) {
				calls.Add(1)
				return db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 500}, nil
			}}
			cfg := testConfig()
			cfg.ManualOnly = manualOnly
			s, err := New(t.Context(), cfg, State{}, b)
			require.NoError(t, err)
			defer s.Close()
			write(s)
			time.Sleep(reclaimCheckInterval - time.Nanosecond)
			synctest.Wait()
			require.Zero(t, b.checks.Load())
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			require.Zero(t, calls.Load(), "reclaimability thresholds still apply")
			if manualOnly {
				require.Zero(t, b.checks.Load())
				return
			}
			require.Equal(t, int64(1), b.checks.Load())
			b.mu.Lock()
			b.free = 500
			b.mu.Unlock()
			s.Begin(false)
			time.Sleep(reclaimCheckInterval)
			synctest.Wait()
			require.Equal(t, int64(2), b.checks.Load(), "recheck without more writes")
			require.Zero(t, calls.Load(), "active reader prevents maintenance")
			s.End(false)
			time.Sleep(cfg.IdleTimeout - time.Nanosecond)
			synctest.Wait()
			require.Zero(t, calls.Load())
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			require.Equal(t, int64(1), calls.Load())
			time.Sleep(reclaimCheckInterval - time.Nanosecond)
			synctest.Wait()
			require.Equal(t, int64(1), calls.Load(), "completed copy resets fallback interval")
		})
	}
}

func TestWriterRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan context.Context, 1)
		finish := make(chan struct{})
		b := &testBackend{size: 100, free: 100, compact: func(ctx context.Context) (db.CompactResult, error) {
			started <- ctx
			select {
			case <-ctx.Done():
				return db.CompactResult{}, context.Cause(ctx)
			case <-finish:
				return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
			}
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: 2}, b)
		require.NoError(t, err)
		defer s.Close()
		for range 2 {
			ctx := <-started
			s.Begin(true)
			synctest.Wait()
			require.ErrorIs(t, context.Cause(ctx), errWriter)
			s.End(true)
		}
		ctx := <-started
		s.Begin(true)
		synctest.Wait()
		require.NoError(t, context.Cause(ctx), "retry limit forces completion")
		close(finish)
		synctest.Wait()
		s.End(true)
		synctest.Wait()
		require.Equal(t, uint64(1), s.state.Writes, "new write belongs to next cycle")
	})
}

func TestCheckpointAndAdaptation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 95}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: 2}, b)
		require.NoError(t, err)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Empty(t, b.checkpoints())
		require.Equal(t, uint64(4), s.state.WriteWatermark)
		write(s)
		time.Sleep(checkpointInterval - time.Second)
		synctest.Wait()
		require.Len(t, b.checkpoints(), 1)
		write(s)
		require.NoError(t, s.Close())
		require.Len(t, b.checkpoints(), 2)
		require.Equal(t, uint64(2), b.checkpoints()[1].Writes)
		s, err = New(t.Context(), testConfig(), b.checkpoints()[1], b)
		require.NoError(t, err)
		require.Equal(t, b.checkpoints()[1], s.state)
		require.NoError(t, s.Close())
	})
}

func TestShutdownCancelsForcedAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		b := &testBackend{size: 100, free: 100, compact: func(ctx context.Context) (db.CompactResult, error) {
			close(started)
			<-ctx.Done()
			return db.CompactResult{}, context.Cause(ctx)
		}}
		cfg := testConfig()
		cfg.MaxRetry = 0
		s, err := New(t.Context(), cfg, State{Writes: 2}, b)
		require.NoError(t, err)
		<-started
		s.Begin(true)
		s.Stop()
		closed := make(chan error, 1)
		go func() { closed <- s.Close() }()
		synctest.Wait()
		require.Empty(t, b.checkpoints())
		s.End(true)
		require.NoError(t, <-closed)
		require.Equal(t, uint64(3), b.checkpoints()[0].Writes)
	})
}

func TestSkippedAttemptBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Reason: "insufficient disk space"}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: 2}, b)
		require.NoError(t, err)
		defer s.Close()
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		write(s)
		time.Sleep(checkpointInterval - time.Second)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal(t, int64(2), calls.Load())
		require.Zero(t, s.retries)
	})
}

func TestSerialCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		finish := make(chan struct{})
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			close(started)
			<-finish
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: 2}, b)
		require.NoError(t, err)
		defer s.Close()
		<-started
		var calls atomic.Int64
		other := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		s2, err := New(t.Context(), testConfig(), State{Writes: 2}, other)
		require.NoError(t, err)
		defer s2.Close()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.Zero(t, calls.Load())
		require.Zero(t, s2.retries)
		close(finish)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
	})
}

func TestWatermarkSaturation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: math.MaxInt64, free: math.MaxInt64, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 100}, nil
		}}
		state := State{Writes: math.MaxUint64, WriteWatermark: math.MaxUint64 - 1}
		s, err := New(t.Context(), testConfig(), state, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Equal(t, uint64(math.MaxUint64), s.state.WriteWatermark)
		require.Zero(t, s.state.Writes)
	})
}

func TestInvalidConfig(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.WriteWatermark = 0 },
		func(c *Config) { c.MinReclaimBytes = 0 },
		func(c *Config) { c.IdleTimeout = 0 },
		func(c *Config) { c.MaxRetry = -1 },
		func(c *Config) { c.MinReclaimPercent = 101 },
	} {
		cfg := testConfig()
		change(&cfg)
		_, err := New(t.Context(), cfg, State{}, &testBackend{})
		require.Error(t, err)
	}
}

func TestReclaimability(t *testing.T) {
	for _, tc := range []struct {
		name              string
		size, free, floor int64
		want              bool
	}{
		{name: "packed", size: 100, free: 0, floor: 10},
		{name: "below bytes", size: 100, free: 25, floor: 26},
		{name: "below percent", size: 100, free: 24, floor: 10},
		{name: "exact thresholds", size: 100, free: 25, floor: 25, want: true},
		{name: "fraction below", size: 101, free: 25, floor: 10},
		{name: "fraction above", size: 101, free: 26, floor: 10, want: true},
		{name: "large below", size: math.MaxInt64, free: math.MaxInt64 / 4, floor: 10},
		{name: "large above", size: math.MaxInt64, free: math.MaxInt64/4 + 1, floor: 10, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int64
				b := &testBackend{size: tc.size, free: tc.free, compact: func(context.Context) (db.CompactResult, error) {
					calls.Add(1)
					return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
				}}
				cfg := testConfig()
				cfg.MinReclaimBytes, cfg.MinReclaimPercent = tc.floor, 25
				s, err := New(t.Context(), cfg, State{Writes: cfg.WriteWatermark}, b)
				require.NoError(t, err)
				defer s.Close()
				time.Sleep(cfg.IdleTimeout)
				synctest.Wait()
				require.Equal(t, tc.want, calls.Load() == 1)
			})
		})
	}
}

func TestRecheckWithoutFileGrowth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		b := &testBackend{size: 100, free: 0, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		cfg := testConfig()
		s, err := New(t.Context(), cfg, State{Writes: cfg.WriteWatermark}, b)
		require.NoError(t, err)
		defer s.Close()
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		b.mu.Lock()
		b.free = 100
		b.mu.Unlock()
		for range 10 {
			write(s)
			synctest.Wait()
		}
		time.Sleep(checkpointInterval - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load(), "writes after a rejected check do not poll stats")
		require.Zero(t, calls.Load())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		require.Equal(t, int64(2), b.checks.Load())
	})
}

func TestObservationTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Scheduler{config: testConfig(), wake: make(chan struct{}, 1), pending: true, lastUse: time.Now()}
		last := s.lastUse
		s.Begin(false)
		time.Sleep(time.Second)
		s.Begin(false)
		s.End(false)
		require.Equal(t, last, s.lastUse)
		require.Empty(t, s.wake, "an overlapping transaction is still active")
		time.Sleep(time.Second)
		s.End(false)
		require.Equal(t, time.Now(), s.lastUse)
		require.Len(t, s.wake, 1)
		<-s.wake
		s.pending = false
		s.state.WriteWatermark = 1
		s.Begin(false)
		write(s)
		require.Len(t, s.wake, 1, "crossing the write watermark wakes even with an active reader")
		<-s.wake
		s.stopping = true
		s.End(false)
		require.Len(t, s.wake, 1, "shutdown wakes when the last transaction ends")
	})
}

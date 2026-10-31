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
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type testBackend struct {
	mu      sync.Mutex
	size    int64
	free    int64
	checks  atomic.Int64
	saved   []State
	opt     db.CompactOptions
	stats   func() (db.CompactionStats, error)
	compact func(context.Context) (db.CompactResult, error)
}

func (b *testBackend) CompactionStats() (db.CompactionStats, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks.Add(1)
	if b.stats != nil {
		return b.stats()
	}
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

func (b *testBackend) Compact(ctx context.Context, opt db.CompactOptions) (db.CompactResult, error) {
	b.mu.Lock()
	b.opt = opt
	b.mu.Unlock()
	return b.compact(ctx)
}

func (b *testBackend) lastOptions() db.CompactOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opt
}

func testConfig() Config {
	return Config{WritesPerCheck: 2, SizeWatermark: 100, SizeGrowthPercent: 100, MinReclaimBytes: 100, IdleTimeout: time.Second, MaxRetry: 2, MinReclaimPercent: 10, MinReclaimPercentFloor: 10}
}

func write(s *Scheduler) {
	s.Begin(true)
	s.End(true)
}

func writeN(s *Scheduler, n uint64) {
	for range n {
		write(s)
	}
}

func writeInterval(s *Scheduler) {
	writeN(s, s.config.WritesPerCheck)
}

func TestTriggersAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		b := &testBackend{size: 100, free: 9, compact: func(context.Context) (db.CompactResult, error) {
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
		writeN(s, s.config.WritesPerCheck-1)
		synctest.Wait()
		require.Zero(t, calls.Load(), "reclaimable floor not reached")
		require.Equal(t, int64(2), b.checks.Load())
		require.Zero(t, s.state.Writes)
		b.mu.Lock()
		b.free = 100
		b.mu.Unlock()
		s.Begin(false)
		writeInterval(s)
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
	})
}

func TestChecksAfterWriteInterval(t *testing.T) {
	for _, manualOnly := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int64
			b := &testBackend{size: 99, free: 99, compact: func(context.Context) (db.CompactResult, error) {
				calls.Add(1)
				return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
			}}
			cfg := testConfig()
			cfg.ManualOnly = manualOnly
			s, err := New(t.Context(), cfg, State{}, b)
			require.NoError(t, err)
			defer s.Close()
			writeN(s, s.config.WritesPerCheck-1)
			time.Sleep(2 * time.Hour)
			synctest.Wait()
			if manualOnly {
				require.Zero(t, b.checks.Load())
				return
			}
			require.Equal(t, int64(1), b.checks.Load(), "the first write samples immediately")
			write(s)
			synctest.Wait()
			require.Equal(t, int64(2), b.checks.Load())
			require.Zero(t, s.state.Writes)
			b.mu.Lock()
			b.size, b.free = 100, 100
			b.mu.Unlock()
			s.Begin(false)
			time.Sleep(2 * time.Hour)
			synctest.Wait()
			require.Equal(t, int64(2), b.checks.Load(), "idle databases are not rechecked")
			writeInterval(s)
			synctest.Wait()
			require.Equal(t, int64(3), b.checks.Load())
			require.Zero(t, calls.Load(), "active reader prevents maintenance")
			s.End(false)
			time.Sleep(cfg.IdleTimeout - time.Nanosecond)
			synctest.Wait()
			require.Zero(t, calls.Load())
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			require.Equal(t, int64(1), calls.Load())
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
		s, err := New(t.Context(), testConfig(), State{Writes: testConfig().WritesPerCheck}, b)
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

func TestCheckpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 95}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: testConfig().WritesPerCheck}, b)
		require.NoError(t, err)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Empty(t, b.checkpoints())
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

func TestCheckpointSkipsUnchangedState(t *testing.T) {
	for _, initial := range []State{
		{},
		{Writes: 1, SizeWatermark: 100},
	} {
		synctest.Test(t, func(t *testing.T) {
			b := &testBackend{}
			cfg := testConfig()
			cfg.ManualOnly = true
			s, err := New(t.Context(), cfg, initial, b)
			require.NoError(t, err)
			time.Sleep(2 * checkpointInterval)
			synctest.Wait()
			require.Empty(t, b.checkpoints())
			write(s)
			time.Sleep(checkpointInterval)
			synctest.Wait()
			require.Len(t, b.checkpoints(), 1)
			time.Sleep(2 * checkpointInterval)
			synctest.Wait()
			require.Len(t, b.checkpoints(), 1)
			require.NoError(t, s.Close())
			require.Len(t, b.checkpoints(), 1)
		})
	}
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
		s, err := New(t.Context(), cfg, State{Writes: cfg.WritesPerCheck}, b)
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
		require.Equal(t, cfg.WritesPerCheck+1, b.checkpoints()[0].Writes)
	})
}

func TestSkippedAttemptBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Reason: "insufficient disk space"}, nil
		}}
		s, err := New(t.Context(), testConfig(), State{Writes: testConfig().WritesPerCheck}, b)
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
		s, err := New(t.Context(), testConfig(), State{Writes: testConfig().WritesPerCheck}, b)
		require.NoError(t, err)
		defer s.Close()
		<-started
		var calls atomic.Int64
		other := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			calls.Add(1)
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		s2, err := New(t.Context(), testConfig(), State{Writes: testConfig().WritesPerCheck}, other)
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

func TestWriteCounterSaturation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		state := State{Writes: math.MaxUint64}
		s, err := New(t.Context(), testConfig(), state, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		time.Sleep(time.Second)
		synctest.Wait()
		require.Zero(t, s.state.Writes)
	})
}

func TestInvalidConfig(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.WritesPerCheck = 0 },
		func(c *Config) { c.SizeWatermark = 0 },
		func(c *Config) { c.SizeGrowthPercent = 0 },
		func(c *Config) { c.MinReclaimBytes = 0 },
		func(c *Config) { c.IdleTimeout = 0 },
		func(c *Config) { c.MaxRetry = -1 },
		func(c *Config) { c.MinReclaimPercent = 101 },
		func(c *Config) { c.MinReclaimPercentFloor = 0 },
		func(c *Config) { c.MinReclaimPercentFloor = 101 },
		func(c *Config) { c.MinReclaimPercentFloor = c.MinReclaimPercent + 1 },
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
		{name: "below both", size: 100, free: 24, floor: 26},
		{name: "bytes reached", size: 1000, free: 100, floor: 100, want: true},
		{name: "bytes reached below percent floor", size: 1000, free: 99, floor: 99},
		{name: "percent reached", size: 100, free: 25, floor: 26, want: true},
		{name: "exact thresholds", size: 100, free: 25, floor: 25, want: true},
		{name: "fraction below", size: 101, free: 25, floor: 26},
		{name: "fraction above", size: 101, free: 26, floor: 100, want: true},
		{name: "large below", size: math.MaxInt64, free: math.MaxInt64 / 4, floor: math.MaxInt64},
		{name: "large above", size: math.MaxInt64, free: math.MaxInt64/4 + 1, floor: math.MaxInt64, want: true},
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
				s, err := New(t.Context(), cfg, State{Writes: cfg.WritesPerCheck}, b)
				require.NoError(t, err)
				defer s.Close()
				time.Sleep(cfg.IdleTimeout)
				synctest.Wait()
				require.Equal(t, tc.want, calls.Load() == 1)
				if tc.want {
					require.Equal(t, db.CompactOptions{
						MinReclaimBytes:        cfg.MinReclaimBytes,
						MinReclaimPercent:      cfg.MinReclaimPercent,
						MinReclaimPercentFloor: cfg.MinReclaimPercentFloor,
					}, b.lastOptions())
				}
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
		s, err := New(t.Context(), cfg, State{SizeWatermark: 200}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		b.mu.Lock()
		b.free = 100
		b.mu.Unlock()
		write(s)
		synctest.Wait()
		time.Sleep(cfg.IdleTimeout)
		synctest.Wait()
		require.Equal(t, int64(1), calls.Load())
		require.Equal(t, int64(2), b.checks.Load())
	})
}

func TestSizeWatermarkGrowth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 75}, nil
		}}
		cfg := testConfig()
		cfg.WritesPerCheck = 10000
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		time.Sleep(cfg.IdleTimeout)
		synctest.Wait()
		require.Equal(t, int64(150), s.state.SizeWatermark)
		require.Zero(t, s.state.Writes)
		require.Equal(t, int64(1), b.checks.Load())
		write(s)
		time.Sleep(sizeCheckInterval - time.Nanosecond)
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load(), "successful compaction resets the interval")
		time.Sleep(time.Nanosecond)
		write(s)
		synctest.Wait()
		require.Equal(t, int64(2), b.checks.Load())
	})
}

func TestSizeCheckInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 99, free: 99, compact: func(context.Context) (db.CompactResult, error) {
			t.Fatal("unexpected compaction")
			return db.CompactResult{}, nil
		}}
		cfg := testConfig()
		cfg.WritesPerCheck = 10000
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		require.Equal(t, uint64(1), s.state.Writes)
		time.Sleep(sizeCheckInterval - time.Nanosecond)
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		time.Sleep(time.Nanosecond)
		write(s)
		synctest.Wait()
		require.Equal(t, int64(2), b.checks.Load())
		require.Equal(t, uint64(3), s.state.Writes)
	})
}

func TestSizeCheckRequiresWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{}
		s, err := New(t.Context(), testConfig(), State{}, b)
		require.NoError(t, err)
		defer s.Close()
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		require.Zero(t, b.checks.Load())
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
	})
}

func TestSizeCheckDueOnlyOnceWhilePending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{size: 100, free: 100, compact: func(context.Context) (db.CompactResult, error) {
			return db.CompactResult{Compacted: true, SizeBefore: 100, SizeAfter: 50}, nil
		}}
		cfg := testConfig()
		cfg.WritesPerCheck = 10000
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		s.Begin(false)
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		require.True(t, s.pending)
		time.Sleep(sizeCheckInterval)
		writeN(s, 100)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		require.True(t, s.sizeDue)
		s.End(false)
	})
}

func TestSizeCheckErrorBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &testBackend{stats: func() (db.CompactionStats, error) {
			return db.CompactionStats{}, errors.New("stat failed")
		}}
		cfg := testConfig()
		cfg.WritesPerCheck = 10000
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		write(s)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		require.True(t, s.sizeDue)
		writeN(s, 100)
		synctest.Wait()
		require.Equal(t, int64(1), b.checks.Load())
		time.Sleep(checkpointInterval)
		synctest.Wait()
		require.Equal(t, int64(2), b.checks.Load())
	})
}

func TestNextWatermark(t *testing.T) {
	require.Equal(t, int64(300), nextWatermark(100, 10, 200))
	require.Equal(t, int64(128), nextWatermark(50, 128, 100))
	require.Equal(t, int64(103), nextWatermark(101, 1, 1))
	require.Equal(t, int64(math.MaxInt64), nextWatermark(math.MaxInt64-1, 1, math.MaxInt64))
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
		s.state.Writes = s.config.WritesPerCheck - 1
		s.Begin(false)
		write(s)
		require.Len(t, s.wake, 1, "crossing the write interval wakes even with an active reader")
		<-s.wake
		s.config.WritesPerCheck = 10
		s.state.Writes = 0
		s.sizeDue = false
		s.lastSizeCheck = time.Now().Add(-sizeCheckInterval)
		write(s)
		require.Len(t, s.wake, 1, "a due size check wakes with an active reader")
		<-s.wake
		write(s)
		require.Empty(t, s.wake, "a due size check wakes only once")
		s.stopping = true
		s.End(false)
		require.Len(t, s.wake, 1, "shutdown wakes when the last transaction ends")
	})
}

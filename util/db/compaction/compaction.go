// Package compaction schedules database maintenance independently of the storage engine.
// Compaction becomes pending after enough committed writes and reclaimable space,
// then waits for an idle period. Arriving writers cancel attempts up to
// the retry limit; subsequent attempts make writers wait. Unproductive compactions
// raise the write watermark. Policy state is checkpointed every five minutes and at close.
package compaction

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
)

const checkpointInterval = 5 * time.Minute

var (
	errWriter = errors.New("compaction interrupted by a writer")
	capacity  = make(chan struct{}, 1)
)

type Config struct {
	ManualOnly        bool
	WriteWatermark    uint64
	MinReclaimBytes   int64
	IdleTimeout       time.Duration
	MaxRetry          int
	MinReclaimPercent int64
	Metrics           *Metrics `json:"-"`
}

func DefaultConfig() Config {
	return Config{
		WriteWatermark:    100000,
		MinReclaimBytes:   256 << 20,
		IdleTimeout:       time.Minute,
		MaxRetry:          3,
		MinReclaimPercent: 25,
	}
}

func (c Config) Validate() error {
	if c.WriteWatermark == 0 || c.MinReclaimBytes <= 0 || c.IdleTimeout <= 0 || c.MaxRetry < 0 || c.MinReclaimPercent <= 0 || c.MinReclaimPercent > 100 {
		return errors.New("invalid database compaction policy")
	}
	return nil
}

type State struct {
	WriteWatermark uint64 `json:"writeWatermark"`
	Writes         uint64 `json:"writes"`
}

type Backend interface {
	CompactionStats() (db.CompactionStats, error)
	Compact(context.Context, db.CompactOptions) (db.CompactResult, error)
	Save(State) error
}

type Scheduler struct {
	config  Config
	backend Backend
	ctx     context.Context
	stop    context.CancelCauseFunc
	wake    chan struct{}
	done    chan struct{}

	mu        sync.Mutex
	state     State
	active    int
	lastUse   time.Time
	notBefore time.Time
	nextCheck time.Time
	pending   bool
	retries   int
	attempt   context.CancelCauseFunc
	stopping  bool
	manual    bool
	requests  chan *Request
	path      string
	metrics   *databaseMetrics
}

// New starts maintenance. Call Close before closing the backend.
func New(ctx context.Context, config Config, state State, backend Backend) (*Scheduler, error) {
	return newScheduler(ctx, config, state, backend, config.Metrics.attach(""))
}

func newScheduler(ctx context.Context, config Config, state State, backend Backend, metrics *databaseMetrics) (*Scheduler, error) {
	if err := config.Validate(); err != nil {
		metrics.close()
		return nil, err
	}
	state.WriteWatermark = max(state.WriteWatermark, config.WriteWatermark)
	ctx, stop := context.WithCancelCause(ctx)
	s := &Scheduler{
		config:   config,
		backend:  backend,
		ctx:      ctx,
		stop:     stop,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		state:    state,
		lastUse:  time.Now(),
		requests: make(chan *Request, 1),
		metrics:  metrics,
	}
	go s.run()
	return s, nil
}

// Begin records access before entering the database's transaction gate.
func (s *Scheduler) Begin(write bool) {
	s.mu.Lock()
	s.active++
	if write && s.attempt != nil && (s.manual || s.retries < s.config.MaxRetry) {
		s.attempt(errWriter)
	}
	s.mu.Unlock()
}

// End counts only committed writes. It must also run after callback errors or panics.
func (s *Scheduler) End(committed bool) {
	s.mu.Lock()
	s.active--
	idle := s.active == 0
	if idle {
		s.lastUse = time.Now()
	}
	crossed := false
	if committed && s.state.Writes < math.MaxUint64 {
		s.state.Writes++
		crossed = s.state.Writes == s.state.WriteWatermark
	}
	wake := idle && (s.pending || s.stopping || s.manual) || crossed
	s.mu.Unlock()
	if wake {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Stop cancels and joins maintenance before the backend is closed.
func (s *Scheduler) Stop() {
	if s.path != "" {
		files.CompareAndDelete(s.path, s)
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	s.stop(errors.WithStack(context.Canceled))
	<-s.done
}

// Close checkpoints after the backend has stopped accepting transactions.
func (s *Scheduler) Close() error {
	s.Stop()
	for {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if active == 0 {
			return s.save()
		}
		<-s.wake
	}
}

func (s *Scheduler) save() error {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	return s.backend.Save(state)
}

func (s *Scheduler) run() {
	defer close(s.done)
	defer s.metrics.close()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.manual = false
		select {
		case r := <-s.requests:
			r.done <- Outcome{Error: "compaction scheduler stopped"}
		default:
		}
	}()
	ticker := time.NewTicker(checkpointInterval)
	defer ticker.Stop()
	timer := time.NewTimer(s.config.IdleTimeout)
	defer timer.Stop()
	for {
		if context.Cause(s.ctx) != nil {
			return
		}
		s.check()
		s.mu.Lock()
		delay := checkpointInterval
		if s.pending && s.active == 0 {
			at := s.lastUse.Add(s.config.IdleTimeout)
			if s.notBefore.After(at) {
				at = s.notBefore
			}
			delay = max(time.Until(at), 0)
		} else if !s.config.ManualOnly && !s.pending && s.state.Writes >= s.state.WriteWatermark {
			delay = max(time.Until(s.nextCheck), 0)
		}
		s.mu.Unlock()
		timer.Reset(delay)
		select {
		case <-s.ctx.Done():
		case <-s.wake:
		case req := <-s.requests:
			s.runRequest(req, ticker.C)
		case <-timer.C:
			s.compact()
		case <-ticker.C:
			if err := s.save(); err != nil {
				bklog.G(s.ctx).WithError(err).Warn("failed to save database compaction policy")
			}
		}
	}
}

func (s *Scheduler) check() {
	if s.config.ManualOnly {
		return
	}
	s.mu.Lock()
	eligible := !s.pending && s.state.Writes >= s.state.WriteWatermark && !time.Now().Before(s.nextCheck)
	if eligible {
		s.nextCheck = time.Now().Add(checkpointInterval)
	}
	s.mu.Unlock()
	if !eligible {
		return
	}
	stats, err := s.stats()
	if err != nil {
		if !errors.Is(err, db.ErrCompactionBusy) {
			bklog.G(s.ctx).WithError(err).Warn("failed to check database compaction watermark")
		}
		return
	}
	percent := s.config.MinReclaimPercent
	if stats.Size > 0 && stats.Reclaimable >= s.config.MinReclaimBytes && stats.Reclaimable >= stats.Size/100*percent+(stats.Size%100*percent+99)/100 {
		s.mu.Lock()
		s.pending = true
		s.metrics.wait(false, true)
		s.mu.Unlock()
	}
}

func (s *Scheduler) compact() {
	s.mu.Lock()
	if s.manual || !s.pending || s.active != 0 || time.Since(s.lastUse) < s.config.IdleTimeout || time.Now().Before(s.notBefore) || context.Cause(s.ctx) != nil {
		s.mu.Unlock()
		return
	}
	// Serialize copies across databases without making a queued writer wait for another DB.
	select {
	case capacity <- struct{}{}:
	default:
		s.notBefore = time.Now().Add(s.config.IdleTimeout)
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	s.attempt = cancel
	s.metrics.wait(false, false)
	writes := s.state.Writes
	s.mu.Unlock()
	res, err := s.backend.Compact(ctx, db.CompactOptions{MinReclaimBytes: s.config.MinReclaimBytes, MinReclaimPercent: s.config.MinReclaimPercent})
	cause := context.Cause(ctx)
	cancel(context.Canceled)
	<-capacity
	s.mu.Lock()
	s.attempt = nil
	s.lastUse = time.Now()
	if res.Compacted {
		s.state.Writes -= writes
		s.pending = false
		s.retries = 0
		if res.SizeAfter > 0 && res.SizeBefore > 0 && float64(res.SizeBefore-res.SizeAfter)/float64(res.SizeBefore)*100 < float64(s.config.MinReclaimPercent) {
			s.state.WriteWatermark += min(s.state.WriteWatermark, math.MaxUint64-s.state.WriteWatermark)
		}
	} else if errors.Is(cause, errWriter) {
		s.retries++
		s.metrics.wait(false, true)
	} else {
		// Disk pressure or a failed drain must not cause a tight retry loop.
		s.pending = false
		s.nextCheck = time.Now().Add(checkpointInterval)
	}
	s.mu.Unlock()
	s.recordAttempt(s.ctx, false, res, err, cause)
	if err != nil && (cause == nil || res.Compacted) {
		bklog.G(s.ctx).WithError(err).Warn("database compaction failed")
	}
	if res.Compacted {
		bklog.G(s.ctx).Infof("compacted database from %d to %d bytes in %s", res.SizeBefore, res.SizeAfter, res.Duration)
	}
}

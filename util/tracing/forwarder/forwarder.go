package forwarder

import (
	"context"
	"sync"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	pendingBufferSize = 1000
	exportTimeout     = 30 * time.Second
)

var (
	errUnstarted      = errors.New("not started")
	errAlreadyStarted = errors.New("already started")
	errShutdown       = errors.New("shutdown")
)

type exportRequest struct {
	deadline time.Time
	spans    []sdktrace.ReadOnlySpan
}

type Exporter struct {
	exp sdktrace.SpanExporter

	ctx      context.Context
	cancel   context.CancelCauseFunc
	wg       sync.WaitGroup
	ch       chan<- exportRequest
	shutdown bool
	mu       sync.RWMutex

	startOnce sync.Once
	stopOnce  sync.Once
}

// New constructs a new Exporter and starts it.
func New(ctx context.Context, exp sdktrace.SpanExporter) (*Exporter, error) {
	e := NewUnstarted(exp)
	if err := e.Start(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// NewUnstarted constructs a new Exporter and does not start it.
func NewUnstarted(exp sdktrace.SpanExporter) *Exporter {
	return &Exporter{exp: exp}
}

// Start marks the Exporter as started.
func (e *Exporter) Start(ctx context.Context) error {
	err := errAlreadyStarted
	e.startOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		e.ctx, e.cancel = context.WithCancelCause(context.Background())
		ch := make(chan exportRequest, pendingBufferSize)
		e.ch = ch
		e.wg.Go(func() {
			e.exportLoop(e.ctx, ch)
		})
		err = nil
	})
	return err
}

func (e *Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.ctx == nil {
		return errUnstarted
	} else if e.shutdown {
		return errShutdown
	}

	select {
	case e.ch <- exportRequest{
		deadline: time.Now().Add(exportTimeout),
		spans:    spans,
	}:
	default:
		bklog.G(ctx).Warnf("dropped %d spans: exporter buffer full", len(spans))
	}
	return nil
}

// exportLoop reads spans from the buffered channel and exports them. It exits
// when ctx is canceled.
func (e *Exporter) exportLoop(ctx context.Context, ch <-chan exportRequest) {
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-ch:
			if !ok {
				return
			}
			e.exportSpans(ctx, req)
		}
	}
}

func (e *Exporter) exportSpans(ctx context.Context, req exportRequest) {
	if time.Since(req.deadline) >= 0 {
		bklog.G(ctx).Warnf("dropped %d spans: export deadline missed", len(req.spans))
	}

	ctx, cancel := context.WithDeadlineCause(ctx, req.deadline, errors.WithStack(context.DeadlineExceeded))
	defer cancel()

	if err := e.exp.ExportSpans(ctx, req.spans); err != nil {
		otel.Handle(err)
	}
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.shutdown = true
	cancel := e.cancel
	e.mu.Unlock()

	if cancel == nil {
		return nil
	}

	var err error
	e.stopOnce.Do(func() {
		// Close the channel to signal that there are no more spans to export.
		close(e.ch)

		// Wait for the spans to drain or the shutdown context to have
		// been canceled. Whichever occurs first.
		var wg sync.WaitGroup
		done := make(chan struct{})
		wg.Go(func() {
			select {
			// All pending exports have finished.
			case <-done:
				// The shutdown deadline has been reached.
			case <-ctx.Done():
			}

			// Either all exports have completed or we've hit
			// our limit for the shutdown context. Cancel the
			// context either way.
			cause := context.Cause(ctx)
			if cause == nil {
				cause = context.Canceled
			}
			cancel(cause)
		})

		// Wait for the run goroutine (and any in-flight export) to
		// finish and then mark the exporter as done when it is.
		e.wg.Wait()
		close(done)

		// Ensure we don't leave orphaned goroutines.
		wg.Wait()

		// Finally finish by calling shutdown on the underlying exporter.
		// If the context already got canceled, this will probably
		// do nothing but just going with it anyway.
		err = e.exp.Shutdown(ctx)
	})
	return err
}

var _ sdktrace.SpanExporter = (*Exporter)(nil)

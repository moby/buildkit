package errdefs

import (
	"context"
	"runtime"
	"sync"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
)

// ExecError will be returned when an error is encountered when evaluating an op.
type ExecError struct {
	error
	Inputs        []solver.Result
	Mounts        []solver.Result
	OwnerBorrowed bool

	mu sync.Mutex // protects OwnerBorrowed
}

func (e *ExecError) Unwrap() error {
	return e.error
}

func (e *ExecError) EachRef(fn func(solver.Result) error) (err error) {
	m := map[solver.Result]struct{}{}
	for _, res := range e.Inputs {
		if res == nil {
			continue
		}
		if _, ok := m[res]; ok {
			continue
		}
		m[res] = struct{}{}
		if err1 := fn(res); err1 != nil && err == nil {
			err = err1
		}
	}
	for _, res := range e.Mounts {
		if res == nil {
			continue
		}
		if _, ok := m[res]; ok {
			continue
		}
		m[res] = struct{}{}
		if err1 := fn(res); err1 != nil && err == nil {
			err = err1
		}
	}
	return err
}

// Borrow marks the error's refs as owned by a caller, so the finalizer and
// Release do not release them. The same error can be shared by multiple
// callers; Borrow returns false if the refs were already borrowed.
func (e *ExecError) Borrow() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.OwnerBorrowed {
		return false
	}
	e.OwnerBorrowed = true
	return true
}

func (e *ExecError) Release() error {
	if !e.Borrow() {
		return nil
	}
	return e.EachRef(func(r solver.Result) error {
		r.Release(context.TODO())
		return nil
	})
}

func WithExecError(err error, inputs, mounts []solver.Result) error {
	return WithExecErrorWithContext(context.TODO(), err, inputs, mounts)
}

func WithExecErrorWithContext(ctx context.Context, err error, inputs, mounts []solver.Result) error {
	if err == nil {
		return nil
	}
	ee := &ExecError{
		error:  err,
		Inputs: inputs,
		Mounts: mounts,
	}
	runtime.SetFinalizer(ee, func(e *ExecError) {
		if e.Borrow() {
			e.EachRef(func(r solver.Result) error {
				bklog.G(ctx).Warn("leaked execError detected and released")
				r.Release(context.TODO())
				return nil
			})
		}
	})
	return ee
}

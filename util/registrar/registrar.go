package registrar

import (
	"context"
	"sync"
)

type Registrar[K comparable, V any] struct {
	mu     sync.Mutex
	values map[K]*registrarValue[V]
}

func New[K comparable, V any]() *Registrar[K, V] {
	return &Registrar[K, V]{
		values: make(map[K]*registrarValue[V]),
	}
}

// Register will register the value with the given id.
// This value will persist until Discard is called with the same id.
func (r *Registrar[K, V]) Register(id K, val V) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getOrCreateRegistrar(id).register(val, nil)
}

// Get retrieves a registered value, waiting until it appears, is discarded, or
// ctx is canceled. An unregistered value is removed when its last waiter leaves.
func (r *Registrar[K, V]) Get(ctx context.Context, id K) (v V, _ error) {
	if err := context.Cause(ctx); err != nil {
		return v, err
	}
	r.mu.Lock()
	reg := r.getOrCreateRegistrar(id)
	reg.waiters++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		reg.waiters--
		// Discard may have removed this entry and a new request reused its ID.
		if reg.waiters == 0 && !reg.isSet && r.values[id] == reg {
			delete(r.values, id)
		}
	}()

	select {
	case <-ctx.Done():
		return v, context.Cause(ctx)
	case <-reg.notifyCh:
		return reg.value, reg.err
	}
}

// Discard will remove the given value from the registrar after it has been registered
// with Register.
func (r *Registrar[K, V]) Discard(id K) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.values[id]
	delete(r.values, id)

	if ok {
		var value V
		reg.register(value, context.Canceled)
	}
}

// getOrCreateRegistrar requires r.mu to be held.
func (r *Registrar[K, V]) getOrCreateRegistrar(id K) *registrarValue[V] {
	reg, ok := r.values[id]
	if !ok {
		reg = &registrarValue[V]{
			notifyCh: make(chan struct{}),
		}
		r.values[id] = reg
	}
	return reg
}

type registrarValue[V any] struct {
	// notifyCh is the notification channel that gets closed when
	// the bridge is registered.
	notifyCh chan struct{}

	value   V
	err     error
	isSet   bool
	waiters int
}

// register requires the owning Registrar's mutex to be held. Published values
// never change; closing notifyCh makes them visible to waiting readers.
func (r *registrarValue[V]) register(value V, err error) {
	if r.isSet {
		return
	}

	r.value = value
	r.err = err
	r.isSet = true
	close(r.notifyCh)
}

package result

import (
	"sync"

	"github.com/pkg/errors"
)

type Result[T comparable] struct {
	Ref          T
	Refs         map[string]T
	Metadata     map[string][]byte
	Attestations map[string][]Attestation[T]
}

func (r *Result[T]) AddMeta(k string, v []byte) {
	if r.Metadata == nil {
		r.Metadata = map[string][]byte{}
	}
	r.Metadata[k] = v
}

func (r *Result[T]) AddRef(k string, ref T) {
	if r.Refs == nil {
		r.Refs = map[string]T{}
	}
	r.Refs[k] = ref
}

func (r *Result[T]) AddAttestation(k string, v Attestation[T]) {
	if r.Attestations == nil {
		r.Attestations = map[string][]Attestation[T]{}
	}
	r.Attestations[k] = append(r.Attestations[k], v)
}

func (r *Result[T]) SetRef(ref T) {
	r.Ref = ref
}

func (r *Result[T]) SingleRef() (T, error) {
	var zero T
	if r.Refs != nil && r.Ref == zero {
		var t T
		return t, errors.Errorf("invalid map result")
	}
	return r.Ref, nil
}

func (r *Result[T]) FindRef(key string) (T, bool) {
	if r.Refs != nil {
		if ref, ok := r.Refs[key]; ok {
			return ref, true
		}
		if len(r.Refs) == 1 {
			for _, ref := range r.Refs {
				return ref, true
			}
		}
		var t T
		return t, false
	}
	return r.Ref, true
}

func (r *Result[T]) EachRef(fn func(T) error) (err error) {
	var zero T
	if r.Ref != zero {
		err = fn(r.Ref)
	}
	for _, r := range r.Refs {
		if r != zero {
			if err1 := fn(r); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	for _, as := range r.Attestations {
		for _, a := range as {
			if a.Ref != zero {
				if err1 := fn(a.Ref); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}

// EachPlatformRef iterates through all underlying refs and invokes
// the given handler with the platform ID corresponding to the ref.
// For the single ref case, the platform ID is an empty string.
func (r *Result[T]) EachPlatformRef(fn func(string, T) error) (err error) {
	var zero T
	if r.Ref != zero {
		err = fn("", r.Ref)
	}
	for p, r := range r.Refs {
		if r != zero {
			if err1 := fn(p, r); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	for p, as := range r.Attestations {
		for _, a := range as {
			if a.Ref != zero {
				if err1 := fn(p, a.Ref); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}

// EachRef iterates over references in both a and b.
// a and b are assumed to be of the same size and map their references
// to the same set of keys
func EachRef[U comparable, V comparable](a *Result[U], b *Result[V], fn func(U, V) error) (err error) {
	var (
		zeroU U
		zeroV V
	)
	if a.Ref != zeroU && b.Ref != zeroV {
		err = fn(a.Ref, b.Ref)
	}
	for k, r := range a.Refs {
		r2, ok := b.Refs[k]
		if !ok {
			continue
		}
		if r != zeroU && r2 != zeroV {
			if err1 := fn(r, r2); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	for k, atts := range a.Attestations {
		atts2, ok := b.Attestations[k]
		if !ok {
			continue
		}
		for i, att := range atts {
			if i >= len(atts2) {
				break
			}
			att2 := atts2[i]
			if att.Ref != zeroU && att2.Ref != zeroV {
				if err1 := fn(att.Ref, att2.Ref); err1 != nil && err == nil {
					err = err1
				}
			}
		}
	}
	return err
}

func ConvertResult[U comparable, V comparable](r *Result[U], fn func(U) (V, error)) (*Result[V], error) {
	var zero U

	r2 := &Result[V]{}
	var err error

	if r.Ref != zero {
		r2.Ref, err = fn(r.Ref)
		if err != nil {
			return nil, err
		}
	}

	if r.Refs != nil {
		r2.Refs = map[string]V{}
	}
	for k, r := range r.Refs {
		if r == zero {
			continue
		}
		r2.Refs[k], err = fn(r)
		if err != nil {
			return nil, err
		}
	}

	if r.Attestations != nil {
		r2.Attestations = map[string][]Attestation[V]{}
	}
	for k, as := range r.Attestations {
		for _, a := range as {
			a2, err := ConvertAttestation(&a, fn)
			if err != nil {
				return nil, err
			}
			r2.Attestations[k] = append(r2.Attestations[k], *a2)
		}
	}

	r2.Metadata = r.Metadata

	return r2, nil
}

// Result returns the underlying Result value
func (r *MutableResult[T]) Result() *Result[T] {
	return &r.res
}

// AddMeta adds the metadata specified with the given key/value pair
func (r *MutableResult[T]) AddMeta(k string, v []byte) {
	r.mu.Lock()
	r.res.AddMeta(k, v)
	r.mu.Unlock()
}

// AddRef adds the specified reference for the platform given with k
func (r *MutableResult[T]) AddRef(k string, ref T) {
	r.mu.Lock()
	r.res.AddRef(k, ref)
	r.mu.Unlock()
}

// SetRef sets the specified reference as the single reference
func (r *MutableResult[T]) SetRef(ref T) {
	r.mu.Lock()
	r.res.SetRef(ref)
	r.mu.Unlock()
}

// MutableResult is a thread-safe version of Result
type MutableResult[T comparable] struct {
	mu  sync.Mutex
	res Result[T]
}

package result

import (
	"reflect"
	"strings"
	"sync"

	"github.com/moby/buildkit/identity"
	"github.com/pkg/errors"
)

const (
	attestationRefPrefix = "attestation:"
)

type Result[T any] struct {
	mu           sync.Mutex
	Ref          T
	Refs         map[string]T
	Metadata     map[string][]byte
	Attestations map[string][]Attestation
}

func (r *Result[T]) AddMeta(k string, v []byte) {
	r.mu.Lock()
	if r.Metadata == nil {
		r.Metadata = map[string][]byte{}
	}
	r.Metadata[k] = v
	r.mu.Unlock()
}

func (r *Result[T]) AddRef(k string, ref T) {
	r.mu.Lock()
	if r.Refs == nil {
		r.Refs = map[string]T{}
	}
	r.Refs[k] = ref
	r.mu.Unlock()
}

func (r *Result[T]) AddAttestation(k string, v Attestation, ref T) {
	r.mu.Lock()
	if r.Attestations == nil {
		r.Attestations = map[string][]Attestation{}
	}
	if !strings.HasPrefix(v.Ref, attestationRefPrefix) {
		v.Ref = "attestation:" + identity.NewID()
		r.Refs[v.Ref] = ref
	}
	r.Attestations[k] = append(r.Attestations[k], v)
	r.mu.Unlock()
}

func (r *Result[T]) SetRef(ref T) {
	r.Ref = ref
}

func (r *Result[T]) SingleRef() (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Refs != nil && !reflect.ValueOf(r.Ref).IsValid() {
		var t T
		return t, errors.Errorf("invalid map result")
	}
	return r.Ref, nil
}

func (r *Result[T]) EachRef(fn func(T) error) (err error) {
	if reflect.ValueOf(r.Ref).IsValid() {
		err = fn(r.Ref)
	}
	for _, r := range r.Refs {
		if reflect.ValueOf(r).IsValid() {
			if err1 := fn(r); err1 != nil && err == nil {
				err = err1
			}
		}
	}
	return err
}

func ConvertResult[U any, V any](r *Result[U], fn func(U) (V, error)) (*Result[V], error) {
	r2 := &Result[V]{}
	var err error

	if reflect.ValueOf(r.Ref).IsValid() {
		r2.Ref, err = fn(r.Ref)
		if err != nil {
			return nil, err
		}
	}

	if r.Refs != nil {
		r2.Refs = map[string]V{}
	}
	for k, r := range r.Refs {
		if reflect.ValueOf(r).IsValid() {
			r2.Refs[k], err = fn(r)
			if err != nil {
				return nil, err
			}
		}
	}

	r2.Attestations = r.Attestations
	r2.Metadata = r.Metadata

	return r2, nil
}

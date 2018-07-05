package client

import (
	"context"
	"sync"

	"github.com/pkg/errors"
)

const defaultRefName = "default"

type BuildFunc func(context.Context, Client) (*Result, error)

type Result struct {
	mu       sync.Mutex
	Refs     map[string]Reference
	Metadata map[string][]byte
}

func NewResult() *Result {
	return &Result{}
}

func (r *Result) AddMeta(k string, v []byte) {
	r.mu.Lock()
	if r.Metadata == nil {
		r.Metadata = map[string][]byte{}
	}
	r.Metadata[k] = v
	r.mu.Unlock()
}

func (r *Result) AddRef(k string, ref Reference) {
	r.mu.Lock()
	if r.Refs == nil {
		r.Refs = map[string]Reference{}
	}
	r.Refs[k] = ref
	r.mu.Unlock()
}

func (r *Result) SetRef(ref Reference) {
	r.AddRef(defaultRefName, ref)
}

func (r *Result) SingleRef() (Reference, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.Refs) == 0 {
		return nil, errors.Errorf("no return references")
	}

	if l := len(r.Refs); l > 1 {
		return nil, errors.Errorf("too many return references: %d", l)
	}

	if _, ok := r.Refs[defaultRefName]; !ok {
		return nil, errors.Errorf("could not find default ref")
	}

	return r.Refs[defaultRefName], nil
}

package client

import (
	"context"
	"sync"
)

type BuildFunc func(context.Context, Client) (*Result, error)

type Result struct {
	mu   sync.Mutex
	Refs map[string]Reference
	Meta map[string]string
}

func NewResult() *Result {
	return &Result{}
}

func (r *Result) AddMeta(k, v string) {
	r.mu.Lock()
	if r.Meta == nil {
		r.Meta = map[string]string{}
	}
	r.Meta[k] = v
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
	r.AddRef("default", ref)
}

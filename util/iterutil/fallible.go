// Package iterutil implements iterator utilities around the
// standard library "iter" package.
package iterutil

import "iter"

// FallibleSeq represents a fallible sequence.
// Use Iterate to iterate over the sequence and Err
// to check the error at the end.
type FallibleSeq[V any] interface {
	Iterate() iter.Seq[V]
	Err() error
}

func FallibleSeqFunc[V any](fn func(yield func(V) bool) error) FallibleSeq[V] {
	return &fallibleSeqFunc[V]{
		fn: fn,
	}
}

type fallibleSeqFunc[V any] struct {
	fn  func(func(V) bool) error
	err error
}

func (f *fallibleSeqFunc[V]) Iterate() iter.Seq[V] {
	return func(yield func(V) bool) {
		f.err = f.fn(yield)
	}
}

func (f *fallibleSeqFunc[V]) Err() error {
	return f.err
}

// FallibleSeq2 is a version of FalliableSeq using
// iter.Seq2 instead.
type FallibleSeq2[K, V any] interface {
	Iterate() iter.Seq2[K, V]
	Err() error
}

func FallibleSeq2Func[K, V any](fn func(yield func(K, V) bool) error) FallibleSeq2[K, V] {
	return &fallibleSeq2Func[K, V]{
		fn: fn,
	}
}

type fallibleSeq2Func[K, V any] struct {
	fn  func(func(K, V) bool) error
	err error
}

func (f *fallibleSeq2Func[K, V]) Iterate() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		f.err = f.fn(yield)
	}
}

func (f *fallibleSeq2Func[K, V]) Err() error {
	return f.err
}

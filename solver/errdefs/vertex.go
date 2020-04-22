package errdefs

import "github.com/opencontainers/go-digest"

type VertexError struct {
	Vertex
	error
}

func (e *VertexError) Unwrap() error {
	return e.error
}

func WrapVertex(err error, dgst digest.Digest) error {
	if err == nil {
		return nil
	}
	return &VertexError{Vertex: Vertex{Digest: dgst.String()}, error: err}
}

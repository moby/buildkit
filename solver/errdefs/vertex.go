package errdefs

import (
	"github.com/moby/buildkit/util/grpcerrors"
	digest "github.com/opencontainers/go-digest"
)

type VertexError struct {
	Vertex
	error
}

func (e *VertexError) Unwrap() error {
	return e.error
}

func (e *VertexError) ToProto() grpcerrors.TypedErrorProto {
	return &e.Vertex
}

func WrapVertex(err error, dgst digest.Digest) error {
	if err == nil {
		return nil
	}
	return &VertexError{Vertex: Vertex{Digest: dgst.String()}, error: err}
}

func (v *Vertex) WrapError(err error) error {
	return &VertexError{error: err, Vertex: *v}
}

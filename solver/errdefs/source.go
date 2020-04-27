package errdefs

import (
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
)

func WithSource(err error, src Source) error {
	if err == nil {
		return nil
	}
	return &ErrorSource{Source: src, error: err}
}

type ErrorSource struct {
	Source
	error
}

func (e *ErrorSource) Unwrap() error {
	return e.error
}

func (e *ErrorSource) ToProto() grpcerrors.TypedErrorProto {
	return &e.Source
}

func Sources(err error) []*Source {
	var out []*Source
	var es *ErrorSource
	if errors.As(err, &es) {
		out = Sources(es.Unwrap())
		out = append(out, &es.Source)
	}
	return out
}

func (s *Source) WrapError(err error) error {
	return &ErrorSource{error: err, Source: *s}
}

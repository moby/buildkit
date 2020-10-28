package errdefs

import "github.com/moby/buildkit/solver/pb"

type OpError struct {
	error
	Op *pb.Op
}

func (e *OpError) Unwrap() error {
	return e.error
}

func WithOp(err error, op *pb.Op) error {
	if err == nil {
		return nil
	}
	return &OpError{error: err, Op: op}
}

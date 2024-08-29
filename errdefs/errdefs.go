package errdefs

import "errors"

//go:generate protoc -I=. -I=../vendor/ -I=../../../../ --go_out=. --go_opt=paths=source_relative errdefs.proto

type internalErr struct {
	error
}

func (internalErr) System() {}

func (err internalErr) Unwrap() error {
	return err.error
}

type system interface {
	System()
}

var _ system = internalErr{}

func Internal(err error) error {
	if err == nil {
		return nil
	}
	return internalErr{err}
}

func IsInternal(err error) bool {
	var s system
	return errors.As(err, &s)
}

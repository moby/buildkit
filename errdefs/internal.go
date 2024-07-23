package errdefs

import (
	"errors"
	"syscall"
)

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
	if errors.As(err, &s) {
		return true
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		if isInternalSyscall(errno) {
			return true
		}
	}
	return false
}

func isInternalSyscall(err syscall.Errno) bool {
	_, ok := syscallErrors()[err]
	return ok
}

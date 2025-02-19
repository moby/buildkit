//nolint:forbidigo
package client

import (
	"errors"
	"fmt"
)

// errConnectionFailed implements an error returned when connection failed.
//
//nolint:errname
type errConnectionFailed struct {
	error
}

// Error returns a string representation of an errConnectionFailed
func (e errConnectionFailed) Error() string {
	return e.error.Error()
}

func (e errConnectionFailed) Unwrap() error {
	return e.error
}

// IsErrConnectionFailed returns true if the error is caused by connection failed.
func IsErrConnectionFailed(err error) bool {
	return errors.As(err, &errConnectionFailed{})
}

// ErrorConnectionFailed returns an error with host in the error message when connection to docker daemon failed.
func ErrorConnectionFailed(host string) error {
	var err error
	if host == "" {
		err = fmt.Errorf("Cannot connect to the Docker daemon. Is the docker daemon running on this host?")
	} else {
		err = fmt.Errorf("Cannot connect to the Docker daemon at %s. Is the docker daemon running?", host)
	}
	return errConnectionFailed{error: err}
}

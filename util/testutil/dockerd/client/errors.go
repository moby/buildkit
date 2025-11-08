package client

import (
	"errors"
	"fmt"
)

// ErrorConnectionFailed returns an error with host in the error message when connection to docker daemon failed.
func ErrorConnectionFailed(host string) error {
	if host == "" {
		return errors.New("Cannot connect to the Docker daemon. Is the docker daemon running on this host?")
	}
	return fmt.Errorf("Cannot connect to the Docker daemon at %s. Is the docker daemon running?", host)
}

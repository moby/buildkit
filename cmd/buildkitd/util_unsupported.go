// +build !linux

package main

import (
	"github.com/docker/docker/pkg/idtools"
	"github.com/pkg/errors"
)

func parseIdentityMapping(str string) (*idtools.IdentityMapping, error) {
	if str == "" {
		return nil, nil
	}
	return nil, errors.New("user namespaces are only supported on linux")
}

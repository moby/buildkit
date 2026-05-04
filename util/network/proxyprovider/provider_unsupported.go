//go:build !linux

package proxyprovider

import (
	"github.com/moby/buildkit/util/network"
	"github.com/pkg/errors"
)

type Opt struct {
	Root     string
	PoolSize int
}

func Supported() bool {
	return false
}

func New(opt Opt) (network.Provider, error) {
	return nil, errors.New("proxy network provider is only supported on linux")
}

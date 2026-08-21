//go:build !linux && !windows

package cniprovider

import (
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
)

func createNetNS(*cniProvider, string) (string, error) {
	return "", errors.New("creating netns for cni not supported")
}

func setNetNS(*specs.Spec, string) error {
	return errors.New("enabling netns for cni not supported")
}

func unmountNetNS(string) error {
	return errors.New("unmounting netns for cni not supported")
}

func deleteNetNS(string) error {
	return errors.New("deleting netns for cni not supported")
}

func cleanOldNamespaces(*cniProvider) {
}

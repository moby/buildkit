//go:build !linux && !windows

package cniprovider

import (
	"errors"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func createNetNS(_ *cniProvider, _ string) (string, error) {
	return "", errors.New("creating netns for cni not supported")
}

func setNetNS(_ *specs.Spec, _ string) error {
	return errors.New("enabling netns for cni not supported")
}

func unmountNetNS(_ string) error {
	return errors.New("unmounting netns for cni not supported")
}

func deleteNetNS(_ string) error {
	return errors.New("deleting netns for cni not supported")
}

func cleanOldNamespaces(_ *cniProvider) {
}

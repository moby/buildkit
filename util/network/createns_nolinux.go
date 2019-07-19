// +build !linux

package network

import "github.com/pkg/errors"

func createNetNS(p string) error {
	return errors.Errorf("creating netns for cni not supported")
}

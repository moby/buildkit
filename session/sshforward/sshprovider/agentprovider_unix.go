//go:build !windows

package sshprovider

import (
	"github.com/pkg/errors"
)

func getFallbackAgentPath() (string, error) {
	return "", errors.New("make sure SSH_AUTH_SOCK is set")
}

func getWindowsPipeDialer(string) *socketDialer {
	return nil
}

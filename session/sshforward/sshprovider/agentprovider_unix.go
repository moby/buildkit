//go:build !windows

package sshprovider

import (
	"errors"
)

func getFallbackAgentPath() (string, error) {
	return "", errors.New("make sure SSH_AUTH_SOCK is set")
}

func getWindowsPipeDialer(_ string) *socketDialer {
	return nil
}

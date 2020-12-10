package sshutil

import (
	"regexp"
)

var gitSSHRegex = regexp.MustCompile("^[a-z0-9]+@[a-zA-Z0-9-.]+:.*$")

func IsSSHTransport(s string) bool {
	return gitSSHRegex.MatchString(s)
}

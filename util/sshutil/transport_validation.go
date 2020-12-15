package sshutil

import (
	"regexp"
	"strings"
)

var gitSSHRegex = regexp.MustCompile("^[a-zA-Z0-9-_]+@[a-zA-Z0-9-.]+:.*$")

func IsSSHTransport(s string) bool {
	// check for explicit ssh transport
	if strings.HasPrefix(s, "ssh://") {
		return true
	}

	// check for implicit ssh transport
	return gitSSHRegex.MatchString(s)
}

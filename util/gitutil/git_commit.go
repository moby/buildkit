package gitutil

import "regexp"

var validHex = regexp.MustCompile(`^[a-f0-9]{40}$`)

func IsCommitSHA(str string) bool {
	return validHex.MatchString(str)
}

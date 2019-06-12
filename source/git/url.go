package git

import (
	"regexp"
)

// based on gopkg.in/src-d/go-git.v4/internal/url that is unfortunately internal

var (
	isSchemeRegExp   = regexp.MustCompile(`^[^:]+://`)
	scpLikeUrlRegExp = regexp.MustCompile(`^(?:(?P<user>[^@]+)@)?(?P<host>[^:\s]+):(?:(?P<port>[0-9]{1,5})/)?(?P<path>[^\\].*)$`)
)

// matchesScheme returns true if the given string matches a URL-like
// format scheme.
func matchesScheme(url string) bool {
	return isSchemeRegExp.MatchString(url)
}

// matchesScpLike returns true if the given string matches an SCP-like
// format scheme.
func matchesScpLike(url string) bool {
	return scpLikeUrlRegExp.MatchString(url)
}

// findScpLikeComponents returns the user, host, port and path of the
// given SCP-like URL.
func findScpLikeComponents(url string) (user, host, port, path string) {
	m := scpLikeUrlRegExp.FindStringSubmatch(url)
	return m[1], m[2], m[3], m[4]
}

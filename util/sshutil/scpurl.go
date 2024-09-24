package sshutil

import (
	"errors"
	"net/url"
	"regexp"
)

var gitSSHRegex = regexp.MustCompile("^(?:([a-zA-Z0-9-_]+)@)?([a-zA-Z0-9-.]+):(.*?)(?:#(.*))?$")

func IsImplicitSSHTransport(s string) bool {
	if u, _ := url.Parse(s); u != nil && u.Scheme != "" && u.Opaque == "" {
		// valid scp-urls do have an explicit scheme
		return false
	}

	return gitSSHRegex.MatchString(s)
}

type SCPStyleURL struct {
	User *url.Userinfo
	Host string

	Path     string
	Fragment string
}

func ParseSCPStyleURL(raw string) (*SCPStyleURL, error) {
	if u, _ := url.Parse(raw); u != nil && u.Scheme != "" && u.Opaque == "" {
		return nil, errors.New("invalid scp-style url: scheme found")
	}

	matches := gitSSHRegex.FindStringSubmatch(raw)
	if matches == nil {
		return nil, errors.New("invalid scp-style url: no match")
	}

	var user *url.Userinfo
	if matches[1] != "" {
		user = url.User(matches[1])
	}
	return &SCPStyleURL{
		User:     user,
		Host:     matches[2],
		Path:     matches[3],
		Fragment: matches[4],
	}, nil
}

func (url *SCPStyleURL) String() string {
	base := url.Host + ":" + url.Path
	if url.User != nil {
		base = url.User.String() + "@" + base
	}
	if url.Fragment == "" {
		return base
	}
	return base + "#" + url.Fragment
}

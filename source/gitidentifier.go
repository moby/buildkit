package source

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

// sshGitRegexp is used to detect if the git repo uses ssh
// e.g. git@... or otheruser@nonstandardgithost.com:my/really/strange/repo.git
var sshGitRegexp, _ = regexp.Compile("[a-z0-9_]+@[^/]+:.+")

type GitIdentifier struct {
	Remote           string
	Ref              string
	Subdir           string
	KeepGitDir       bool
	AuthTokenSecret  string
	AuthHeaderSecret string
	MountSSHSock     bool
	KnownSSHHosts    string
}

func NewGitIdentifier(remoteURL string) (*GitIdentifier, error) {
	repo := GitIdentifier{}

	var fragment string
	if sshGitRegexp.MatchString(remoteURL) {
		// git@.. is not an URL, so cannot be parsed as URL
		parts := strings.SplitN(remoteURL, "#", 2)

		repo.Remote = parts[0]
		if len(parts) == 2 {
			fragment = parts[1]
		}
		repo.Ref, repo.Subdir = getRefAndSubdir(fragment)
	} else {
		if !strings.HasPrefix(remoteURL, "http://") && !strings.HasPrefix(remoteURL, "https://") {
			remoteURL = "https://" + remoteURL
		}

		u, err := url.Parse(remoteURL)
		if err != nil {
			return nil, err
		}

		repo.Ref, repo.Subdir = getRefAndSubdir(u.Fragment)
		u.Fragment = ""
		repo.Remote = u.String()
	}
	if repo.Subdir != "" {
		return nil, errors.Errorf("subdir not supported yet")
	}
	return &repo, nil
}

func (i *GitIdentifier) ID() string {
	return "git"
}

func getRefAndSubdir(fragment string) (ref string, subdir string) {
	refAndDir := strings.SplitN(fragment, ":", 2)
	ref = "master"
	if len(refAndDir[0]) != 0 {
		ref = refAndDir[0]
	}
	if len(refAndDir) > 1 && len(refAndDir[1]) != 0 {
		subdir = refAndDir[1]
	}
	return
}

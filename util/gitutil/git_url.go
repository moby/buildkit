package gitutil

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/moby/buildkit/util/sshutil"
	"github.com/pkg/errors"
)

const (
	HTTPProtocol  string = "http"
	HTTPSProtocol string = "https"
	SSHProtocol   string = "ssh"
	GitProtocol   string = "git"
)

var (
	ErrUnknownProtocol  = errors.New("unknown protocol")
	ErrInvalidProtocol  = errors.New("invalid protocol")
	ErrImplicitUsername = errors.New("implicit username not permitted")
)

var supportedProtos = map[string]struct{}{
	HTTPProtocol:  {},
	HTTPSProtocol: {},
	SSHProtocol:   {},
	GitProtocol:   {},
}

var protoRegexp = regexp.MustCompile(`^[a-zA-Z0-9]+://`)

// URL is a custom URL type that points to a remote Git repository.
//
// URLs can be parsed from both standard URLs (e.g.
// "https://github.com/moby/buildkit.git"), as well as SCP-like URLs (e.g.
// "git@github.com:moby/buildkit.git").
//
// See https://git-scm.com/book/en/v2/Git-on-the-Server-The-Protocols
type GitURL struct {
	// Scheme is the protocol over which the git repo can be accessed
	Scheme string

	// Host is the remote host that hosts the git repo
	Host string
	// Path is the path on the host to access the repo
	Path string
	// User is the username/password to access the host
	User *url.Userinfo
	// Fragment can contain additional metadata
	Fragment *GitURLFragment

	// Remote is a valid URL remote to pass into the Git CLI tooling (i.e.
	// without the fragment metadata)
	Remote string
	// RemoteURL is the original URL parsed from a standard URL (but only if it
	// was parsed from one)
	RemoteURL *url.URL
	// RemoteSCPStyleURL is the original URL parsed from an scyp-style URL (but
	// only if it was parsed from one)
	RemoteSCPStyleURL *sshutil.SCPStyleURL
}

// GitURLFragment is the buildkit-specific metadata extracted from the fragment
// of a remote URL.
type GitURLFragment struct {
	// Ref is the git reference
	Ref string
	// Subdir is the sub-directory inside the git repository to use
	Subdir string
}

// splitGitFragment splits a git URL fragment into its respective git
// reference and subdirectory components.
func splitGitFragment(fragment string) *GitURLFragment {
	if fragment == "" {
		return nil
	}
	ref, subdir, _ := strings.Cut(fragment, ":")
	return &GitURLFragment{Ref: ref, Subdir: subdir}
}

type parseURLOpts struct {
	noImplicitSCPUsername bool
}
type ParseURLOpt func(*parseURLOpts)

// NoImplicitSCPUsername prevents parsing a URL that has no username specified.
func NoImplicitSCPUsername() ParseURLOpt {
	return func(opts *parseURLOpts) {
		opts.noImplicitSCPUsername = true
	}
}

// ParseURL parses a BuildKit-style Git URL (that may contain additional
// fragment metadata) and returns a parsed GitURL object.
func ParseURL(remote string, opts ...ParseURLOpt) (*GitURL, error) {
	opt := parseURLOpts{}
	for _, f := range opts {
		f(&opt)
	}

	if proto := protoRegexp.FindString(remote); proto != "" {
		proto = strings.ToLower(strings.TrimSuffix(proto, "://"))
		if _, ok := supportedProtos[proto]; !ok {
			return nil, errors.Wrap(ErrInvalidProtocol, proto)
		}
		url, err := url.Parse(remote)
		if err != nil {
			return nil, err
		}
		return fromURL(url), nil
	}

	if url, err := sshutil.ParseSCPStyleURL(remote); err == nil {
		if opt.noImplicitSCPUsername && url.User == nil {
			return nil, ErrImplicitUsername
		}
		return fromSCPStyleURL(url), nil
	}

	return nil, ErrUnknownProtocol
}

func IsGitTransport(remote string, opts ...ParseURLOpt) bool {
	_, err := ParseURL(remote, opts...)
	return err == nil
}

func fromURL(url *url.URL) *GitURL {
	withoutFragment := *url
	withoutFragment.Fragment = ""
	return &GitURL{
		Scheme:    url.Scheme,
		User:      url.User,
		Host:      url.Host,
		Path:      url.Path,
		Fragment:  splitGitFragment(url.Fragment),
		Remote:    withoutFragment.String(),
		RemoteURL: url,
	}
}

func fromSCPStyleURL(url *sshutil.SCPStyleURL) *GitURL {
	withoutFragment := *url
	withoutFragment.Fragment = ""
	return &GitURL{
		Scheme:            SSHProtocol,
		User:              url.User,
		Host:              url.Host,
		Path:              url.Path,
		Fragment:          splitGitFragment(url.Fragment),
		Remote:            withoutFragment.String(),
		RemoteSCPStyleURL: url,
	}
}

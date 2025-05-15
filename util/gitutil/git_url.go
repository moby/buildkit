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
	ErrUnknownProtocol = errors.New("unknown protocol")
	ErrInvalidProtocol = errors.New("invalid protocol")
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
	// Opts can contain additional metadata
	Opts *GitURLOpts

	// Remote is a valid URL remote to pass into the Git CLI tooling (i.e.
	// without the fragment metadata)
	Remote string
}

// GitURLOpts is the buildkit-specific metadata extracted from the fragment
// or the query of a remote URL.
type GitURLOpts struct {
	// Ref is the git reference
	Ref string
	// Subdir is the sub-directory inside the git repository to use
	Subdir string
}

// parseOpts splits a git URL fragment into its respective git
// reference and subdirectory components.
func parseOpts(fragment string, query url.Values) (*GitURLOpts, error) {
	if fragment == "" && len(query) == 0 {
		return nil, nil
	}
	opts := &GitURLOpts{}
	if fragment != "" {
		opts.Ref, opts.Subdir, _ = strings.Cut(fragment, ":")
	}
	var tag, branch string
	for k, v := range query {
		switch len(v) {
		case 0:
			return nil, errors.Errorf("query %q has no value", k)
		case 1:
			if v[0] == "" {
				return nil, errors.Errorf("query %q has no value", k)
			}
			// NOP
		default:
			return nil, errors.Errorf("query %q has multiple values", k)
		}
		switch k {
		case "ref":
			if opts.Ref != "" && opts.Ref != v[0] {
				return nil, errors.Errorf("ref conflicts: %q vs %q", opts.Ref, v[0])
			}
			opts.Ref = v[0]
		case "tag":
			tag = v[0]
		case "branch":
			branch = v[0]
		case "subdir":
			if opts.Subdir != "" && opts.Subdir != v[0] {
				return nil, errors.Errorf("subdir conflicts: %q vs %q", opts.Subdir, v[0])
			}
			opts.Subdir = v[0]
		default:
			return nil, errors.Errorf("unexpected query %q", k)
		}
	}
	if tag != "" {
		if opts.Ref != "" {
			return nil, errors.New("tag conflicts with ref")
		}
		opts.Ref = "refs/tags/" + tag
	}
	if branch != "" {
		if tag != "" {
			// TODO: consider allowing this, when the tag actually exists on the branch
			return nil, errors.New("branch conflicts with tag")
		}
		if opts.Ref != "" {
			return nil, errors.New("branch conflicts with ref")
		}
		opts.Ref = "refs/heads/" + branch
	}
	return opts, nil
}

// ParseURL parses a BuildKit-style Git URL (that may contain additional
// fragment metadata) and returns a parsed GitURL object.
func ParseURL(remote string) (*GitURL, error) {
	if proto := protoRegexp.FindString(remote); proto != "" {
		proto = strings.ToLower(strings.TrimSuffix(proto, "://"))
		if _, ok := supportedProtos[proto]; !ok {
			return nil, errors.Wrap(ErrInvalidProtocol, proto)
		}
		url, err := url.Parse(remote)
		if err != nil {
			return nil, err
		}
		return FromURL(url)
	}

	if url, err := sshutil.ParseSCPStyleURL(remote); err == nil {
		return fromSCPStyleURL(url)
	}

	return nil, ErrUnknownProtocol
}

func IsGitTransport(remote string) bool {
	if proto := protoRegexp.FindString(remote); proto != "" {
		proto = strings.ToLower(strings.TrimSuffix(proto, "://"))
		_, ok := supportedProtos[proto]
		return ok
	}
	return sshutil.IsImplicitSSHTransport(remote)
}

func FromURL(url *url.URL) (*GitURL, error) {
	withoutOpts := *url
	withoutOpts.Fragment = ""
	withoutOpts.RawQuery = ""
	opts, err := parseOpts(url.Fragment, url.Query())
	if err != nil {
		return nil, err
	}
	return &GitURL{
		Scheme: url.Scheme,
		User:   url.User,
		Host:   url.Host,
		Path:   url.Path,
		Opts:   opts,
		Remote: withoutOpts.String(),
	}, nil
}

func fromSCPStyleURL(url *sshutil.SCPStyleURL) (*GitURL, error) {
	withoutOpts := *url
	withoutOpts.Fragment = ""
	withoutOpts.Query = nil
	opts, err := parseOpts(url.Fragment, url.Query)
	if err != nil {
		return nil, err
	}
	return &GitURL{
		Scheme: SSHProtocol,
		User:   url.User,
		Host:   url.Host,
		Path:   url.Path,
		Opts:   opts,
		Remote: withoutOpts.String(),
	}, nil
}

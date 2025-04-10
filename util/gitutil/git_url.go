package gitutil

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/moby/buildkit/util/sshutil"
	"github.com/pkg/errors"
	"github.com/tonistiigi/go-csvvalue"
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
	// Fragment can contain additional metadata
	Fragment *GitURLFragment

	// Remote is a valid URL remote to pass into the Git CLI tooling (i.e.
	// without the fragment metadata)
	Remote string
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
func splitGitFragment(fragment string) (*GitURLFragment, error) {
	if strings.HasPrefix(fragment, "#") {
		// Double-hash in the unparsed URL.
		// e.g., https://github.com/user/repo.git##ref=tag,subdir=/dir
		return splitGitFragmentCSVForm(fragment)
	}
	// Single-hash in the unparsed URL.
	// e.g., https://github.com/user/repo.git#branch_or_tag_or_commit:dir
	if fragment == "" {
		return nil, nil
	}
	ref, subdir, _ := strings.Cut(fragment, ":")
	return &GitURLFragment{Ref: ref, Subdir: subdir}, nil
}

func splitGitFragmentCSVForm(fragment string) (*GitURLFragment, error) {
	fragment = strings.TrimPrefix(fragment, "#")
	if fragment == "" {
		return nil, nil
	}
	fields, err := csvvalue.Fields(fragment, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse CSV %q", fragment)
	}

	res := &GitURLFragment{}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return nil, errors.Errorf("invalid field '%s' must be a key=value pair", field)
		}
		key = strings.ToLower(key)
		switch key {
		case "ref":
			res.Ref = value
		case "subdir":
			res.Subdir = value
		default:
			return nil, errors.Errorf("unexpected key '%s' in '%s' (supported keys: ref, subdir)", key, field)
		}
	}
	return res, nil
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
		return fromURL(url)
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

// ErrInvalidURLFragemnt is returned for an invalid URL fragment
type ErrInvalidURLFragemnt struct {
	error
}

func fromURL(url *url.URL) (*GitURL, error) {
	withoutFragment := *url
	withoutFragment.Fragment = ""
	fragment, err := splitGitFragment(url.Fragment)
	if err != nil {
		return nil, &ErrInvalidURLFragemnt{
			error: errors.Wrapf(err, "failed to parse URL fragment %q", url.Fragment),
		}
	}
	gitURL := &GitURL{
		Scheme:   url.Scheme,
		User:     url.User,
		Host:     url.Host,
		Path:     url.Path,
		Fragment: fragment,
		Remote:   withoutFragment.String(),
	}
	return gitURL, nil
}

func fromSCPStyleURL(url *sshutil.SCPStyleURL) (*GitURL, error) {
	withoutFragment := *url
	withoutFragment.Fragment = ""
	fragment, err := splitGitFragment(url.Fragment)
	if err != nil {
		return nil, &ErrInvalidURLFragemnt{
			error: errors.Wrapf(err, "failed to parse URL fragment %q", url.Fragment),
		}
	}
	return &GitURL{
		Scheme:   SSHProtocol,
		User:     url.User,
		Host:     url.Host,
		Path:     url.Path,
		Fragment: fragment,
		Remote:   withoutFragment.String(),
	}, nil
}

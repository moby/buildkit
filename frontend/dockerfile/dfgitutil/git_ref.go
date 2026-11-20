// Package dfgitutil provides Dockerfile-specific utilities for git refs.
package dfgitutil

import (
	"net/url"
	"path"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/util/gitutil"
	"github.com/pkg/errors"
)

// GitRef represents a git ref.
//
// Examples:
//   - "https://github.com/foo/bar.git#baz/qux:quux/quuz" is parsed into:
//     {Remote: "https://github.com/foo/bar.git", ShortName: "bar", Commit:"baz/qux", SubDir: "quux/quuz"}.
type GitRef struct {
	// Remote is the remote repository path.
	Remote string

	// ShortName is the directory name of the repo.
	// e.g., "bar" for "https://github.com/foo/bar.git"
	ShortName string

	// Ref is a commit hash, a tag, or branch name.
	// Ref is optional.
	Ref string

	// Checksum is a commit hash.
	Checksum string

	// SubDir is a directory path inside the repo.
	// SubDir is optional.
	SubDir string

	// IndistinguishableFromLocal is true for a ref that is indistinguishable from a local file path,
	// e.g., "github.com/foo/bar".
	//
	// Deprecated.
	// Instead, use a distinguishable form such as "https://github.com/foo/bar.git".
	//
	// The dockerfile frontend still accepts this form only for build contexts.
	IndistinguishableFromLocal bool

	// UnencryptedTCP is true for a ref that needs an unencrypted TCP connection,
	// e.g., "git://..." and "http://..." .
	//
	// Discouraged, although not deprecated.
	// Instead, consider using an encrypted TCP connection such as "git@github.com/foo/bar.git" or "https://github.com/foo/bar.git".
	UnencryptedTCP bool

	// KeepGitDir is true for URL that controls whether to keep the .git directory.
	KeepGitDir *bool

	// Submodules is true for URL that controls whether to fetch git submodules.
	Submodules *bool

	// MTime controls file modification time policy: "checkout" (default) or "commit".
	MTime string

	// FetchByCommit, when true, trusts Checksum as the commit and skips comparing
	// it against the remote ref. The commit is fetched directly and the ref name
	// (if any) is applied locally.
	FetchByCommit bool
}

// cutGitSchemePrefix strips the "git+" scheme prefix from ref,
// which explicitly marks an HTTP(S) URL as a git repository URL,
// e.g., "git+https://git.sr.ht/~foo/bar".
// It reports whether ref had the prefix, and returns an error when the prefixed
// scheme is neither HTTP nor HTTPS.
func cutGitSchemePrefix(ref string) (string, bool, error) {
	if len(ref) < len("git+") || !strings.EqualFold(ref[:len("git+")], "git+") {
		return ref, false, nil
	}
	after := ref[len("git+"):]
	scheme, _, ok := strings.Cut(after, "://")
	if !ok {
		// Not a URL, e.g., a local path such as "git+foo"
		return ref, false, nil
	}
	switch strings.ToLower(scheme) {
	case gitutil.HTTPProtocol, gitutil.HTTPSProtocol:
		return after, true, nil
	}
	return "", true, errors.Wrapf(gitutil.ErrInvalidProtocol, "git+%s (only git+http and git+https are supported)", scheme)
}

// ParseGitRef parses a git ref.
//
// An HTTP(S) URL is considered to be a git ref when its path has the ".git" suffix,
// or when its scheme has the "git+" prefix, e.g., "git+https://git.sr.ht/~foo/bar".
// The "git+" prefix is meant for git forges that do not support the ".git" suffix.
func ParseGitRef(ref string) (*GitRef, bool, error) {
	res := &GitRef{}

	ref, explicitGit, err := cutGitSchemePrefix(ref)
	if err != nil {
		return nil, true, err
	}

	var remote *gitutil.GitURL

	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return nil, false, errors.WithStack(cerrdefs.ErrInvalidArgument)
	} else if strings.HasPrefix(ref, "github.com/") {
		res.IndistinguishableFromLocal = true // Deprecated
		u, err := url.Parse(ref)
		if err != nil {
			return nil, false, err
		}
		u.Scheme = "https"
		remote, err = gitutil.FromURL(u)
		if err != nil {
			return nil, false, err
		}
	} else {
		remote, err = gitutil.ParseURL(ref)
		if err != nil {
			// A "git+" prefixed URL is a git ref, even when it is malformed
			return nil, explicitGit, err
		}

		switch remote.Scheme {
		case gitutil.HTTPProtocol, gitutil.GitProtocol:
			res.UnencryptedTCP = true // Discouraged, but not deprecated
		}

		switch remote.Scheme {
		// An HTTP(S) URL is considered to be a valid git ref only when it has the ".git[...]" suffix,
		// or when it is explicitly marked as such with the "git+" scheme prefix.
		case gitutil.HTTPProtocol, gitutil.HTTPSProtocol:
			if !explicitGit && !strings.HasSuffix(remote.Path, ".git") {
				return nil, false, errors.WithStack(cerrdefs.ErrInvalidArgument)
			}
		}
	}

	res.Remote = remote.Remote
	if res.IndistinguishableFromLocal {
		_, res.Remote, _ = strings.Cut(res.Remote, "://")
	}
	if remote.Opts != nil {
		res.Ref, res.SubDir = remote.Opts.Ref, remote.Opts.Subdir
	}

	repoSplitBySlash := strings.Split(res.Remote, "/")
	res.ShortName = strings.TrimSuffix(repoSplitBySlash[len(repoSplitBySlash)-1], ".git")

	if err := res.loadQuery(remote.Query); err != nil {
		return nil, true, err
	}

	return res, true, nil
}

func (gf *GitRef) loadQuery(query url.Values) error {
	if len(query) == 0 {
		return nil
	}
	var tag, branch string
	for k, v := range query {
		switch len(v) {
		case 0, 1:
			if len(v) == 0 || v[0] == "" {
				switch k {
				case "submodules", "keep-git-dir", "fetch-by-commit":
					v = nil
				default:
					return errors.Errorf("query %q has no value", k)
				}
			}
			// NOP
		default:
			return errors.Errorf("query %q has multiple values", k)
		}
		switch k {
		case "ref":
			if gf.Ref != "" && gf.Ref != v[0] {
				return errors.Errorf("ref conflicts: %q vs %q", gf.Ref, v[0])
			}
			gf.Ref = v[0]
		case "tag":
			tag = v[0]
		case "branch":
			branch = v[0]
		case "subdir":
			if gf.SubDir != "" && gf.SubDir != v[0] {
				return errors.Errorf("subdir conflicts: %q vs %q", gf.SubDir, v[0])
			}
			gf.SubDir = v[0]
		case "checksum", "commit":
			gf.Checksum = v[0]
		case "keep-git-dir":
			var vv bool
			if len(v) == 0 {
				vv = true
			} else {
				var err error
				vv, err = strconv.ParseBool(v[0])
				if err != nil {
					return errors.Errorf("invalid keep-git-dir value: %q", v[0])
				}
			}
			gf.KeepGitDir = &vv
		case "submodules":
			var vv bool
			if len(v) == 0 {
				vv = true
			} else {
				var err error
				vv, err = strconv.ParseBool(v[0])
				if err != nil {
					return errors.Errorf("invalid submodules value: %q", v[0])
				}
			}
			gf.Submodules = &vv
		case "mtime":
			switch v[0] {
			case "checkout", "commit":
				gf.MTime = v[0]
			default:
				return errors.Errorf("invalid mtime value: %q (must be \"checkout\" or \"commit\")", v[0])
			}
		case "fetch-by-commit":
			var vv bool
			if len(v) == 0 {
				vv = true
			} else {
				var err error
				vv, err = strconv.ParseBool(v[0])
				if err != nil {
					return errors.Errorf("invalid fetch-by-commit value: %q", v[0])
				}
			}
			gf.FetchByCommit = vv
		default:
			return errors.Errorf("unexpected query %q", k)
		}
	}
	if tag != "" {
		const tagPrefix = "refs/tags/"
		if !strings.HasPrefix(tag, tagPrefix) {
			tag = tagPrefix + tag
		}
		if gf.Ref != "" && gf.Ref != tag {
			return errors.Errorf("ref conflicts: %q vs %q", gf.Ref, tag)
		}
		gf.Ref = tag
	}
	if branch != "" {
		if tag != "" {
			// TODO: consider allowing this, when the tag actually exists on the branch
			return errors.New("branch conflicts with tag")
		}
		const branchPrefix = "refs/heads/"
		if !strings.HasPrefix(branch, branchPrefix) {
			branch = branchPrefix + branch
		}
		if gf.Ref != "" && gf.Ref != branch {
			return errors.Errorf("ref conflicts: %q vs %q", gf.Ref, branch)
		}
		gf.Ref = branch
	}
	return nil
}

// FragmentFormat returns a simplified git URL in fragment format.
// If the URL cannot be parsed, the original string is returned with false.
func FragmentFormat(remote string, withSubdir bool) (string, bool) {
	gitRef, _, err := ParseGitRef(remote)
	if err != nil || gitRef == nil {
		return remote, false
	}
	u := gitRef.Remote
	subdir := ""
	if withSubdir {
		subdir = strings.TrimPrefix(path.Join("/", gitRef.SubDir), "/")
	}
	if gitRef.Ref != "" || subdir != "" {
		u += "#" + gitRef.Ref
		if subdir != "" {
			u += ":" + subdir
		}
	}
	return u, true
}

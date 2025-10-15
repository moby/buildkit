package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	srctypes "github.com/moby/buildkit/source/types"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/gitutil"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/util/urlutil"
	"github.com/moby/locker"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var defaultBranch = regexp.MustCompile(`refs/heads/(\S+)`)

type Opt struct {
	CacheAccessor cache.Accessor
}

type Source struct {
	cache  cache.Accessor
	locker *locker.Locker
}

type Metadata struct {
	Ref            string
	Checksum       string
	CommitChecksum string
}

// Supported returns nil if the system supports Git source
func Supported() error {
	if err := exec.Command("git", "version").Run(); err != nil {
		return errors.Wrap(err, "failed to find git binary")
	}
	return nil
}

func NewSource(opt Opt) (*Source, error) {
	gs := &Source{
		cache:  opt.CacheAccessor,
		locker: locker.New(),
	}
	return gs, nil
}

func (gs *Source) Schemes() []string {
	return []string{srctypes.GitScheme}
}

func (gs *Source) Identifier(scheme, ref string, attrs map[string]string, platform *pb.Platform) (source.Identifier, error) {
	id, err := NewGitIdentifier(ref)
	if err != nil {
		return nil, err
	}

	for k, v := range attrs {
		switch k {
		case pb.AttrKeepGitDir:
			if v == "true" {
				id.KeepGitDir = true
			}
		case pb.AttrFullRemoteURL:
			if !gitutil.IsGitTransport(v) {
				v = "https://" + v
			}
			id.Remote = v
		case pb.AttrAuthHeaderSecret:
			id.AuthHeaderSecret = v
		case pb.AttrAuthTokenSecret:
			id.AuthTokenSecret = v
		case pb.AttrKnownSSHHosts:
			id.KnownSSHHosts = v
		case pb.AttrMountSSHSock:
			id.MountSSHSock = v
		case pb.AttrGitChecksum:
			id.Checksum = v
		case pb.AttrGitSkipSubmodules:
			if v == "true" {
				id.SkipSubmodules = true
			}
		}
	}

	return id, nil
}

// needs to be called with repo lock
func (gs *Source) mountRemote(ctx context.Context, remote string, authArgs []string, sha256 bool, reset bool, g session.Group) (target string, release func() error, retErr error) {
	sis, err := searchGitRemote(ctx, gs.cache, remote)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to search metadata for %s", urlutil.RedactCredentials(remote))
	}

	var remoteRef cache.MutableRef
	for _, si := range sis {
		if reset {
			if err := si.clearGitRemote(); err != nil {
				bklog.G(ctx).Warnf("failed to clear git remote metadata for %s %s: %v", urlutil.RedactCredentials(remote), si.ID(), err)
			}
		} else {
			remoteRef, err = gs.cache.GetMutable(ctx, si.ID())
			if err != nil {
				if errors.Is(err, cache.ErrLocked) {
					// should never really happen as no other function should access this metadata, but lets be graceful
					bklog.G(ctx).Warnf("mutable ref for %s  %s was locked: %v", urlutil.RedactCredentials(remote), si.ID(), err)
					continue
				}
				return "", nil, errors.Wrapf(err, "failed to get mutable ref for %s", urlutil.RedactCredentials(remote))
			}
			break
		}
	}

	initializeRepo := false
	if remoteRef == nil {
		remoteRef, err = gs.cache.New(ctx, nil, g, cache.CachePolicyRetain, cache.WithDescription(fmt.Sprintf("shared git repo for %s", urlutil.RedactCredentials(remote))))
		if err != nil {
			return "", nil, errors.Wrapf(err, "failed to create new mutable for %s", urlutil.RedactCredentials(remote))
		}
		initializeRepo = true
	}

	releaseRemoteRef := func() error {
		return remoteRef.Release(context.TODO())
	}

	defer func() {
		if retErr != nil && remoteRef != nil {
			releaseRemoteRef()
		}
	}()

	mount, err := remoteRef.Mount(ctx, false, g)
	if err != nil {
		return "", nil, err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return "", nil, err
	}

	defer func() {
		if retErr != nil {
			lm.Unmount()
		}
	}()

	git := gitCLI(
		gitutil.WithGitDir(dir),
		gitutil.WithArgs(authArgs...),
	)

	if initializeRepo {
		// Explicitly set the Git config 'init.defaultBranch' to the
		// implied default to suppress "hint:" output about not having a
		// default initial branch name set which otherwise spams unit
		// test logs.
		args := []string{"-c", "init.defaultBranch=master", "init", "--bare"}
		if sha256 {
			args = append(args, "--object-format=sha256")
		}
		if _, err := git.Run(ctx, args...); err != nil {
			return "", nil, errors.Wrapf(err, "failed to init repo at %s", dir)
		}

		if _, err := git.Run(ctx, "remote", "add", "origin", remote); err != nil {
			return "", nil, errors.Wrapf(err, "failed add origin repo at %s", dir)
		}

		// save new remote metadata
		md := cacheRefMetadata{remoteRef}
		if err := md.setGitRemote(remote); err != nil {
			return "", nil, err
		}
	}
	return dir, func() error {
		err := lm.Unmount()
		if err1 := releaseRemoteRef(); err == nil {
			err = err1
		}
		return err
	}, nil
}

type gitSourceHandler struct {
	*Source
	src         GitIdentifier
	cacheKey    string
	cacheCommit string
	sha256      bool
	sm          *session.Manager
	authArgs    []string
}

func (gs *gitSourceHandler) shaToCacheKey(sha, ref string) string {
	key := sha
	if gs.src.KeepGitDir {
		key += ".git"
		if ref != "" {
			key += "#" + ref
		}
	}
	if gs.src.Subdir != "" {
		key += ":" + gs.src.Subdir
	}
	if gs.src.SkipSubmodules {
		key += "(skip-submodules)"
	}
	return key
}

func (gs *Source) ResolveMetadata(ctx context.Context, id *GitIdentifier, sm *session.Manager, jobCtx solver.JobContext) (*Metadata, error) {
	gsh := &gitSourceHandler{
		src:    *id,
		Source: gs,
		sm:     sm,
	}
	md, err := gsh.resolveMetadata(ctx, jobCtx)
	if err != nil {
		return nil, err
	}
	return md, nil
}

func (gs *Source) Resolve(ctx context.Context, id source.Identifier, sm *session.Manager, _ solver.Vertex) (source.SourceInstance, error) {
	gitIdentifier, ok := id.(*GitIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid git identifier %v", id)
	}

	return &gitSourceHandler{
		src:    *gitIdentifier,
		Source: gs,
		sm:     sm,
	}, nil
}

type authSecret struct {
	token bool
	name  string
}

func (gs *gitSourceHandler) authSecretNames() (sec []authSecret, _ error) {
	u, err := url.Parse(gs.src.Remote)
	if err != nil {
		return nil, err
	}
	if gs.src.AuthHeaderSecret != "" {
		sec = append(sec, authSecret{name: gs.src.AuthHeaderSecret + "." + u.Host})
	}
	if gs.src.AuthTokenSecret != "" {
		sec = append(sec, authSecret{name: gs.src.AuthTokenSecret + "." + u.Host, token: true})
	}
	if gs.src.AuthHeaderSecret != "" {
		sec = append(sec, authSecret{name: gs.src.AuthHeaderSecret})
	}
	if gs.src.AuthTokenSecret != "" {
		sec = append(sec, authSecret{name: gs.src.AuthTokenSecret, token: true})
	}
	return sec, nil
}

func (gs *gitSourceHandler) getAuthToken(ctx context.Context, g session.Group) error {
	if gs.authArgs != nil {
		return nil
	}
	sec, err := gs.authSecretNames()
	if err != nil {
		return err
	}
	err = gs.sm.Any(ctx, g, func(ctx context.Context, _ string, caller session.Caller) error {
		var err error
		for _, s := range sec {
			var dt []byte
			dt, err = secrets.GetSecret(ctx, caller, s.name)
			if err != nil {
				if errors.Is(err, secrets.ErrNotFound) {
					continue
				}
				return err
			}
			if s.token {
				dt = []byte("basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "x-access-token:%s", dt)))
			}
			gs.authArgs = []string{"-c", "http." + tokenScope(gs.src.Remote) + ".extraheader=Authorization: " + string(dt)}
			break
		}
		return err
	})
	if errors.Is(err, secrets.ErrNotFound) {
		err = nil
	}
	return err
}

func (gs *gitSourceHandler) mountSSHAuthSock(ctx context.Context, sshID string, g session.Group) (string, func() error, error) {
	var caller session.Caller
	err := gs.sm.Any(ctx, g, func(ctx context.Context, _ string, c session.Caller) error {
		if err := sshforward.CheckSSHID(ctx, c, sshID); err != nil {
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
				return errors.Errorf("no SSH key %q forwarded from the client", sshID)
			}

			return err
		}
		caller = c
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	usr, err := user.Current()
	if err != nil {
		return "", nil, err
	}

	// best effort, default to root
	uid, _ := strconv.Atoi(usr.Uid)
	gid, _ := strconv.Atoi(usr.Gid)

	sock, cleanup, err := sshforward.MountSSHSocket(ctx, caller, sshforward.SocketOpt{
		ID:   sshID,
		UID:  uid,
		GID:  gid,
		Mode: 0700,
	})
	if err != nil {
		return "", nil, err
	}

	return sock, cleanup, nil
}

func (gs *gitSourceHandler) mountKnownHosts() (string, func() error, error) {
	if gs.src.KnownSSHHosts == "" {
		return "", nil, errors.Errorf("no configured known hosts forwarded from the client")
	}
	knownHosts, err := os.CreateTemp("", "")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() error {
		return os.Remove(knownHosts.Name())
	}
	_, err = knownHosts.Write([]byte(gs.src.KnownSSHHosts))
	if err != nil {
		cleanup()
		return "", nil, err
	}
	err = knownHosts.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return knownHosts.Name(), cleanup, nil
}

func (gs *gitSourceHandler) remoteKey() string {
	return gs.src.Remote + "#" + gs.src.Ref
}

func (gs *gitSourceHandler) resolveMetadata(ctx context.Context, jobCtx solver.JobContext) (md *Metadata, retErr error) {
	remote := gs.src.Remote
	gs.locker.Lock(remote)
	defer gs.locker.Unlock(remote)

	if gs.src.Checksum != "" {
		matched, err := regexp.MatchString("^[a-fA-F0-9]+$", gs.src.Checksum)
		if err != nil || !matched {
			return nil, errors.Errorf("invalid checksum %s for Git URL, expected hex commit hash", gs.src.Checksum)
		}
	}

	if gitutil.IsCommitSHA(gs.src.Ref) {
		if gs.src.Checksum != "" && !strings.HasPrefix(gs.src.Ref, gs.src.Checksum) {
			return nil, errors.Errorf("expected checksum to match %s, got %s", gs.src.Checksum, gs.src.Ref)
		}
		return &Metadata{
			Ref:      gs.src.Ref,
			Checksum: gs.src.Ref,
		}, nil
	}

	var g session.Group
	if jobCtx != nil {
		g = jobCtx.Session()

		if rc := jobCtx.ResolverCache(); rc != nil {
			values, release, err := rc.Lock(gs.remoteKey())
			if err != nil {
				return nil, err
			}
			saveResolved := true
			defer func() {
				v := md
				if retErr != nil || !saveResolved {
					v = nil
				}
				if err := release(v); err != nil {
					bklog.G(ctx).Warnf("failed to release resolver cache lock for %s: %v", gs.remoteKey(), err)
				}
			}()
			for _, v := range values {
				v2, ok := v.(*Metadata)
				if !ok {
					return nil, errors.Errorf("invalid resolver cache value for %s: %T", gs.remoteKey(), v)
				}
				if gs.src.Checksum != "" && !strings.HasPrefix(v2.Checksum, gs.src.Checksum) {
					continue
				}
				saveResolved = false
				clone := *v2
				return &clone, nil
			}
		}
	}

	gs.getAuthToken(ctx, g)

	tmpGit, cleanup, err := gs.emptyGitCli(ctx, g)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	ref := gs.src.Ref
	if ref == "" {
		ref, err = getDefaultBranch(ctx, tmpGit, gs.src.Remote)
		if err != nil {
			return nil, err
		}
	}

	// TODO: should we assume that remote tag is immutable? add a timer?

	buf, err := tmpGit.Run(ctx, "ls-remote", gs.src.Remote, ref, ref+"^{}")
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch remote %s", urlutil.RedactCredentials(remote))
	}
	lines := strings.Split(string(buf), "\n")

	var (
		partialRef      = "refs/" + strings.TrimPrefix(ref, "refs/")
		headRef         = "refs/heads/" + strings.TrimPrefix(ref, "refs/heads/")
		tagRef          = "refs/tags/" + strings.TrimPrefix(ref, "refs/tags/")
		annotatedTagRef = tagRef + "^{}" // dereferenced annotated tag
	)
	var sha, headSha, tagSha, annotatedTagSha string
	var usedRef string

	for _, line := range lines {
		lineSha, lineRef, _ := strings.Cut(line, "\t")
		switch lineRef {
		case headRef:
			headSha = lineSha
		case tagRef:
			tagSha = lineSha
		case annotatedTagRef:
			annotatedTagSha = lineSha
		case partialRef:
			sha = lineSha
			usedRef = lineRef
		}
	}

	// git-checkout prefers branches in case of ambiguity
	if sha == "" {
		sha = headSha
		usedRef = headRef
	}
	if sha != "" {
		annotatedTagSha = "" // ignore annotated tag if branch or commit matched
	}
	if sha == "" {
		sha = tagSha
		usedRef = tagRef
	}
	if sha == "" {
		return nil, errors.Errorf("repository does not contain ref %s, output: %q", ref, string(buf))
	}
	if !gitutil.IsCommitSHA(sha) {
		return nil, errors.Errorf("invalid commit sha %q", sha)
	}
	if gs.src.Checksum != "" {
		if !strings.HasPrefix(sha, gs.src.Checksum) && !strings.HasPrefix(annotatedTagSha, gs.src.Checksum) {
			exp := sha
			if annotatedTagSha != "" {
				exp = " or " + annotatedTagSha
			}
			return nil, errors.Errorf("expected checksum to match %s, got %s", gs.src.Checksum, exp)
		}
	}
	md = &Metadata{
		Ref:      usedRef,
		Checksum: sha,
	}
	if annotatedTagSha != "" && !gs.src.KeepGitDir {
		// prefer commit sha pointed by annotated tag if no git dir is kept for more matches
		md.CommitChecksum = annotatedTagSha
	}
	return md, nil
}

func (gs *gitSourceHandler) CacheKey(ctx context.Context, jobCtx solver.JobContext, index int) (string, string, solver.CacheOpts, bool, error) {
	md, err := gs.resolveMetadata(ctx, jobCtx)
	if err != nil {
		return "", "", nil, false, err
	}

	if gitutil.IsCommitSHA(md.Ref) {
		cacheKey := gs.shaToCacheKey(md.Ref, md.Ref)
		gs.cacheKey = cacheKey
		gs.cacheCommit = md.Ref
		gs.sha256 = len(md.Ref) == 64
		// gs.src.Checksum is verified when checking out the commit
		return cacheKey, md.Ref, nil, true, nil
	}

	shaForCacheKey := md.Checksum
	if md.CommitChecksum != "" && !gs.src.KeepGitDir {
		// prefer commit sha pointed by annotated tag if no git dir is kept for more matches
		shaForCacheKey = md.CommitChecksum
	}
	cacheKey := gs.shaToCacheKey(shaForCacheKey, md.Ref)
	gs.cacheKey = cacheKey
	gs.sha256 = len(md.Checksum) == 64
	gs.cacheCommit = md.Checksum
	return cacheKey, md.Checksum, nil, true, nil
}

func (gs *gitSourceHandler) Snapshot(ctx context.Context, jobCtx solver.JobContext) (cache.ImmutableRef, error) {
	cacheKey := gs.cacheKey
	if cacheKey == "" {
		var err error
		cacheKey, _, _, _, err = gs.CacheKey(ctx, jobCtx, 0)
		if err != nil {
			return nil, err
		}
	}

	var g session.Group
	if jobCtx != nil {
		g = jobCtx.Session()
	}
	gs.getAuthToken(ctx, g)

	snapshotKey := cacheKey + ":" + gs.src.Subdir
	gs.locker.Lock(snapshotKey)
	defer gs.locker.Unlock(snapshotKey)

	sis, err := searchGitSnapshot(ctx, gs.cache, snapshotKey)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search metadata for %s", snapshotKey)
	}
	if len(sis) > 0 {
		return gs.cache.Get(ctx, sis[0].ID(), nil)
	}

	gs.locker.Lock(gs.src.Remote)
	defer gs.locker.Unlock(gs.src.Remote)

	ref, err := gs.trySnapshot(ctx, g, false)
	if err != nil {
		var wce *wouldClobberExistingTagError
		var ulre *unableToUpdateLocalRefError
		if errors.As(err, &wce) || errors.As(err, &ulre) {
			ref, err = gs.trySnapshot(ctx, g, true)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	md := cacheRefMetadata{ref}
	if err := md.setGitSnapshot(snapshotKey); err != nil {
		return nil, err
	}
	return ref, nil
}

func (gs *gitSourceHandler) trySnapshot(ctx context.Context, g session.Group, reset bool) (out cache.ImmutableRef, retErr error) {
	git, cleanup, err := gs.emptyGitCli(ctx, g)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	gitDir, unmountGitDir, err := gs.mountRemote(ctx, gs.src.Remote, gs.authArgs, gs.sha256, reset, g)
	if err != nil {
		return nil, err
	}
	defer unmountGitDir()
	git = git.New(gitutil.WithGitDir(gitDir))

	ref := gs.src.Ref
	if ref == "" {
		ref, err = getDefaultBranch(ctx, git, gs.src.Remote)
		if err != nil {
			return nil, err
		}
	}

	doFetch := true
	if gitutil.IsCommitSHA(ref) {
		// skip fetch if commit already exists
		if _, err := git.Run(ctx, "cat-file", "-e", ref+"^{commit}"); err == nil {
			doFetch = false
		}
	}

	// local refs are needed so they would be advertised on next fetches. Force is used
	// in case the ref is a branch and it now points to a different commit sha
	// TODO: is there a better way to do this?
	targetRef := ref
	if !strings.HasPrefix(ref, "refs/tags/") {
		targetRef = "tags/" + ref
	}

	if doFetch {
		// make sure no old lock files have leaked
		os.RemoveAll(filepath.Join(gitDir, "shallow.lock"))

		args := []string{"fetch"}
		if !gitutil.IsCommitSHA(ref) { // TODO: find a branch from ls-remote?
			args = append(args, "--depth=1", "--no-tags")
		} else {
			args = append(args, "--tags")
			if _, err := os.Lstat(filepath.Join(gitDir, "shallow")); err == nil {
				args = append(args, "--unshallow")
			}
		}
		args = append(args, "origin")
		if gitutil.IsCommitSHA(ref) {
			args = append(args, ref)
		} else {
			args = append(args, "--force", ref+":"+targetRef)
		}
		if _, err := git.Run(ctx, args...); err != nil {
			err := errors.Wrapf(err, "failed to fetch remote %s", urlutil.RedactCredentials(gs.src.Remote))
			if strings.Contains(err.Error(), "rejected") && strings.Contains(err.Error(), "(would clobber existing tag)") {
				// this can happen if a tag was mutated to another commit in remote.
				// only hope is to abandon the existing shared repo and start a fresh one
				return nil, &wouldClobberExistingTagError{err}
			}
			if strings.Contains(err.Error(), "(unable to update local ref)") && strings.Contains(err.Error(), "some local refs could not be updated;") {
				// this can happen if a branch updated in remote so that old branch
				// is now a parent dir of a new branch
				return nil, &unableToUpdateLocalRefError{err}
			}

			return nil, err
		}

		// verify that commit matches the cache key commit
		dt, err := git.Run(ctx, "rev-parse", ref)
		if err != nil {
			return nil, err
		}
		// if fetched ref does not match cache key, the remote side has changed the ref
		// if possible we can try to force the commit that the cache key points to, otherwise we need to error
		if strings.TrimSpace(string(dt)) != gs.cacheCommit {
			uptRef := targetRef
			if !strings.HasPrefix(uptRef, "refs/") {
				uptRef = "refs/" + uptRef
			}
			// check if the commit still exists in the repo
			if _, err := git.Run(ctx, "cat-file", "-e", gs.cacheCommit); err == nil {
				// force the ref to point to the commit that the cache key points to
				if _, err := git.Run(ctx, "update-ref", uptRef, gs.cacheCommit, "--no-deref"); err != nil {
					return nil, err
				}
			} else {
				// try to fetch the commit directly
				args := []string{"fetch", "--tags"}
				if _, err := os.Lstat(filepath.Join(gitDir, "shallow")); err == nil {
					args = append(args, "--unshallow")
				}
				args = append(args, "origin", gs.cacheCommit)
				if _, err := git.Run(ctx, args...); err != nil {
					return nil, errors.Wrapf(err, "failed to fetch remote %s", urlutil.RedactCredentials(gs.src.Remote))
				}

				_, err = git.Run(ctx, "reflog", "expire", "--all", "--expire=now")
				if err != nil {
					return nil, errors.Wrapf(err, "failed to expire reflog for remote %s", urlutil.RedactCredentials(gs.src.Remote))
				}
				if _, err := git.Run(ctx, "cat-file", "-e", gs.cacheCommit); err == nil {
					// force the ref to point to the commit that the cache key points to
					if _, err := git.Run(ctx, "update-ref", uptRef, gs.cacheCommit, "--no-deref"); err != nil {
						return nil, err
					}
				} else {
					return nil, errors.Errorf("fetched ref %s does not match expected commit %s and commit can not be found in the repository", ref, gs.cacheCommit)
				}
			}
		}
	}

	checkoutRef, err := gs.cache.New(ctx, nil, g, cache.WithRecordType(client.UsageRecordTypeGitCheckout), cache.WithDescription(fmt.Sprintf("git snapshot for %s#%s", urlutil.RedactCredentials(gs.src.Remote), ref)))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create new mutable for %s", urlutil.RedactCredentials(gs.src.Remote))
	}

	defer func() {
		if retErr != nil && checkoutRef != nil {
			checkoutRef.Release(context.WithoutCancel(ctx))
		}
	}()

	mount, err := checkoutRef.Mount(ctx, false, g)
	if err != nil {
		return nil, err
	}
	lm := snapshot.LocalMounter(mount)
	checkoutDir, err := lm.Mount()
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil && lm != nil {
			lm.Unmount()
		}
	}()

	subdir := path.Clean(gs.src.Subdir)
	if subdir == "/" {
		subdir = "."
	}

	if gs.src.Checksum != "" {
		actualHashBuf, err := git.Run(ctx, "rev-parse", ref)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to rev-parse %s for %s", ref, urlutil.RedactCredentials(gs.src.Remote))
		}
		actualHash := strings.TrimSpace(string(actualHashBuf))
		if !strings.HasPrefix(actualHash, gs.src.Checksum) {
			retErr := errors.Errorf("expected checksum to match %s, got %s", gs.src.Checksum, actualHash)
			actualHashBuf2, err := git.Run(ctx, "rev-parse", ref+"^{}")
			if err != nil {
				return nil, retErr
			}
			actualHash2 := strings.TrimSpace(string(actualHashBuf2))
			if actualHash2 == actualHash {
				return nil, retErr
			}
			if !strings.HasPrefix(actualHash2, gs.src.Checksum) {
				return nil, errors.Errorf("expected checksum to match %s, got %s or %s", gs.src.Checksum, actualHash, actualHash2)
			}
		}
	}

	cd := checkoutDir
	if gs.src.KeepGitDir && subdir == "." {
		checkoutDirGit := filepath.Join(checkoutDir, ".git")
		if err := os.MkdirAll(checkoutDir, 0711); err != nil {
			return nil, err
		}
		checkoutGit := git.New(gitutil.WithWorkTree(checkoutDir), gitutil.WithGitDir(checkoutDirGit))
		args := []string{"-c", "init.defaultBranch=master", "init"}
		if gs.sha256 {
			args = append(args, "--object-format=sha256")
		}
		_, err = checkoutGit.Run(ctx, args...)
		if err != nil {
			return nil, err
		}
		// Defense-in-depth: clone using the file protocol to disable local-clone
		// optimizations which can be abused on some versions of Git to copy unintended
		// host files into the build context.
		_, err = checkoutGit.Run(ctx, "remote", "add", "origin", "file://"+gitDir)
		if err != nil {
			return nil, err
		}

		gitCatFileBuf, err := git.Run(ctx, "cat-file", "-t", ref)
		if err != nil {
			return nil, err
		}
		isAnnotatedTag := strings.TrimSpace(string(gitCatFileBuf)) == "tag"

		pullref := ref
		if isAnnotatedTag {
			targetRef := pullref
			if !strings.HasPrefix(pullref, "refs/tags/") {
				targetRef = "refs/tags/" + pullref
			}
			pullref += ":" + targetRef
		} else if gitutil.IsCommitSHA(ref) {
			pullref = "refs/buildkit/" + identity.NewID()
			_, err = git.Run(ctx, "update-ref", pullref, ref)
			if err != nil {
				return nil, err
			}
		} else {
			pullref += ":" + pullref
		}
		_, err = checkoutGit.Run(ctx, "fetch", "-u", "--depth=1", "origin", pullref)
		if err != nil {
			return nil, err
		}
		_, err = checkoutGit.Run(ctx, "checkout", "FETCH_HEAD")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to checkout remote %s", urlutil.RedactCredentials(gs.src.Remote))
		}
		_, err = checkoutGit.Run(ctx, "remote", "set-url", "origin", urlutil.RedactCredentials(gs.src.Remote))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to set remote origin to %s", urlutil.RedactCredentials(gs.src.Remote))
		}
		_, err = checkoutGit.Run(ctx, "reflog", "expire", "--all", "--expire=now")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to expire reflog for remote %s", urlutil.RedactCredentials(gs.src.Remote))
		}
		if err := os.Remove(filepath.Join(checkoutDirGit, "FETCH_HEAD")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errors.Wrapf(err, "failed to remove FETCH_HEAD for remote %s", urlutil.RedactCredentials(gs.src.Remote))
		}
		gitDir = checkoutDirGit
	} else {
		if subdir != "." {
			cd, err = os.MkdirTemp(cd, "checkout")
			if err != nil {
				return nil, errors.Wrapf(err, "failed to create temporary checkout dir")
			}
		}
		checkoutGit := git.New(gitutil.WithWorkTree(cd), gitutil.WithGitDir(gitDir))
		_, err = checkoutGit.Run(ctx, "checkout", ref, "--", ".")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to checkout remote %s", urlutil.RedactCredentials(gs.src.Remote))
		}
	}

	git = git.New(gitutil.WithWorkTree(cd), gitutil.WithGitDir(gitDir))
	if !gs.src.SkipSubmodules {
		_, err = git.Run(ctx, "submodule", "update", "--init", "--recursive", "--depth=1")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to update submodules for %s", urlutil.RedactCredentials(gs.src.Remote))
		}
	}

	if subdir != "." {
		d, err := os.Open(filepath.Join(cd, subdir))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to open subdir %v", subdir)
		}
		defer func() {
			if d != nil {
				d.Close()
			}
		}()
		names, err := d.Readdirnames(0)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if err := os.Rename(filepath.Join(cd, subdir, n), filepath.Join(checkoutDir, n)); err != nil {
				return nil, err
			}
		}
		if err := d.Close(); err != nil {
			return nil, err
		}
		d = nil // reset defer
		if err := os.RemoveAll(cd); err != nil {
			return nil, err
		}
	}

	if idmap := mount.IdentityMapping(); idmap != nil {
		uid, gid := idmap.RootPair()
		err := filepath.WalkDir(gitDir, func(p string, _ os.DirEntry, _ error) error {
			return os.Lchown(p, uid, gid)
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to remap git checkout")
		}
	}

	lm.Unmount()
	lm = nil

	snap, err := checkoutRef.Commit(ctx)
	if err != nil {
		return nil, err
	}
	checkoutRef = nil

	defer func() {
		if retErr != nil {
			snap.Release(context.WithoutCancel(ctx))
		}
	}()

	return snap, nil
}

type wouldClobberExistingTagError struct {
	error
}

func (e *wouldClobberExistingTagError) Unwrap() error {
	return e.error
}

type unableToUpdateLocalRefError struct {
	error
}

func (e *unableToUpdateLocalRefError) Unwrap() error {
	return e.error
}

func (gs *gitSourceHandler) emptyGitCli(ctx context.Context, g session.Group, opts ...gitutil.Option) (*gitutil.GitCLI, func() error, error) {
	var cleanups []func() error
	cleanup := func() error {
		var err error
		for _, c := range cleanups {
			if err1 := c(); err == nil {
				err = err1
			}
		}
		cleanups = nil
		return err
	}
	var err error

	var sock string
	if gs.src.MountSSHSock != "" {
		var unmountSock func() error
		sock, unmountSock, err = gs.mountSSHAuthSock(ctx, gs.src.MountSSHSock, g)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		cleanups = append(cleanups, unmountSock)
	}

	var knownHosts string
	if gs.src.KnownSSHHosts != "" {
		var unmountKnownHosts func() error
		knownHosts, unmountKnownHosts, err = gs.mountKnownHosts()
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		cleanups = append(cleanups, unmountKnownHosts)
	}

	opts = append([]gitutil.Option{
		gitutil.WithArgs(gs.authArgs...),
		gitutil.WithSSHAuthSock(sock),
		gitutil.WithSSHKnownHosts(knownHosts),
	}, opts...)
	return gitCLI(opts...), cleanup, err
}

func tokenScope(remote string) string {
	// generally we can only use the token for fetching main remote but in case of github.com we do best effort
	// to try reuse same token for all github.com remotes. This is the same behavior actions/checkout uses
	for _, pfx := range []string{"https://github.com/", "https://www.github.com/"} {
		if strings.HasPrefix(remote, pfx) {
			return pfx
		}
	}
	return remote
}

// getDefaultBranch gets the default branch of a repository using ls-remote
func getDefaultBranch(ctx context.Context, git *gitutil.GitCLI, remoteURL string) (string, error) {
	buf, err := git.Run(ctx, "ls-remote", "--symref", remoteURL, "HEAD")
	if err != nil {
		return "", errors.Wrapf(err, "error fetching default branch for repository %s", urlutil.RedactCredentials(remoteURL))
	}

	ss := defaultBranch.FindAllStringSubmatch(string(buf), -1)
	if len(ss) == 0 || len(ss[0]) != 2 {
		return "", errors.Errorf("could not find default branch for repository: %s", urlutil.RedactCredentials(remoteURL))
	}
	return ss[0][1], nil
}

const (
	keyGitRemote     = "git-remote"
	gitRemoteIndex   = keyGitRemote + "::"
	keyGitSnapshot   = "git-snapshot"
	gitSnapshotIndex = keyGitSnapshot + "::"
)

func search(ctx context.Context, store cache.MetadataStore, key string, idx string) ([]cacheRefMetadata, error) {
	var results []cacheRefMetadata
	mds, err := store.Search(ctx, idx+key, false)
	if err != nil {
		return nil, err
	}
	for _, md := range mds {
		results = append(results, cacheRefMetadata{md})
	}
	return results, nil
}

func searchGitRemote(ctx context.Context, store cache.MetadataStore, remote string) ([]cacheRefMetadata, error) {
	return search(ctx, store, remote, gitRemoteIndex)
}

func searchGitSnapshot(ctx context.Context, store cache.MetadataStore, key string) ([]cacheRefMetadata, error) {
	return search(ctx, store, key, gitSnapshotIndex)
}

type cacheRefMetadata struct {
	cache.RefMetadata
}

func (md cacheRefMetadata) setGitSnapshot(key string) error {
	return md.SetString(keyGitSnapshot, key, gitSnapshotIndex+key)
}

func (md cacheRefMetadata) setGitRemote(key string) error {
	return md.SetString(keyGitRemote, key, gitRemoteIndex+key)
}

func (md cacheRefMetadata) clearGitRemote() error {
	return md.ClearValueAndIndex(keyGitRemote, gitRemoteIndex)
}

func gitCLI(opts ...gitutil.Option) *gitutil.GitCLI {
	opts = append([]gitutil.Option{
		gitutil.WithExec(runWithStandardUmask),
		gitutil.WithStreams(func(ctx context.Context) (stdout, stderr io.WriteCloser, flush func()) {
			return logs.NewLogStreams(ctx, false)
		}),
	}, opts...)
	return gitutil.NewGitCLI(opts...)
}

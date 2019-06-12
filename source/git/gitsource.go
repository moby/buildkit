package git

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/pkg/locker"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/src-d/go-billy.v4/osfs"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/config"
	"gopkg.in/src-d/go-git.v4/plumbing"
	gitssh "gopkg.in/src-d/go-git.v4/plumbing/transport/ssh"
)

var validHex = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Opt struct {
	CacheAccessor cache.Accessor
	MetadataStore *metadata.Store
}

type gitSource struct {
	md     *metadata.Store
	cache  cache.Accessor
	locker *locker.Locker
}

func NewSource(opt Opt) (source.Source, error) {
	gs := &gitSource{
		md:     opt.MetadataStore,
		cache:  opt.CacheAccessor,
		locker: locker.New(),
	}
	return gs, nil
}

func (gs *gitSource) ID() string {
	return source.GitScheme
}

// needs to be called with repo lock
func (gs *gitSource) mountRemote(ctx context.Context, remote string) (gitDir string, repo *git.Repository, release func(), retErr error) {
	remoteKey := "git-remote::" + remote

	sis, err := gs.md.Search(remoteKey)
	if err != nil {
		return "", nil, nil, errors.Wrapf(err, "failed to search metadata for %s", remote)
	}

	var remoteRef cache.MutableRef
	for _, si := range sis {
		remoteRef, err = gs.cache.GetMutable(ctx, si.ID())
		if err != nil {
			if cache.IsLocked(err) {
				// should never really happen as no other function should access this metadata, but lets be graceful
				logrus.Warnf("mutable ref for %s  %s was locked: %v", remote, si.ID(), err)
				continue
			}
			return "", nil, nil, errors.Wrapf(err, "failed to get mutable ref for %s", remote)
		}
		break
	}

	initializeRepo := false
	if remoteRef == nil {
		remoteRef, err = gs.cache.New(ctx, nil, cache.CachePolicyRetain, cache.WithDescription(fmt.Sprintf("shared git repo for %s", remote)))
		if err != nil {
			return "", nil, nil, errors.Wrapf(err, "failed to create new mutable for %s", remote)
		}
		initializeRepo = true
	}

	releaseRemoteRef := func() {
		remoteRef.Release(context.TODO())
	}

	defer func() {
		if retErr != nil && remoteRef != nil {
			releaseRemoteRef()
		}
	}()

	mount, err := remoteRef.Mount(ctx, false)
	if err != nil {
		return "", nil, nil, err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return "", nil, nil, err
	}

	defer func() {
		if retErr != nil {
			lm.Unmount()
		}
	}()

	if initializeRepo {
		repo, err = git.PlainInit(dir, true)
		if err != nil {
			return "", nil, nil, errors.WithStack(err)
		}

		_, err := repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{remote},
		})
		if err != nil {
			return "", nil, nil, errors.WithStack(err)
		}

		// same new remote metadata
		si, _ := gs.md.Get(remoteRef.ID())
		v, err := metadata.NewValue(remoteKey)
		v.Index = remoteKey
		if err != nil {
			return "", nil, nil, err
		}

		if err := si.Update(func(b *bolt.Bucket) error {
			return si.SetValue(b, "git-remote", v)
		}); err != nil {
			return "", nil, nil, err
		}
	} else {
		repo, err = git.PlainOpen(dir)
		if err != nil {
			return "", nil, nil, errors.WithStack(err)
		}
	}
	return dir, repo, func() {
		lm.Unmount()
		releaseRemoteRef()
	}, nil
}

func getSSHCaller(ctx context.Context, sm *session.Manager) (session.Caller, error) {
	sessionID := session.FromContext(ctx)
	if sessionID == "" {
		return nil, errors.New("could not access ssh agent without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := sm.Get(timeoutCtx, sessionID)
	if err != nil {
		return nil, err
	}

	id := "default"

	if err := sshforward.CheckSSHID(ctx, caller, id); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
			return nil, errors.Errorf("no SSH key %q forwarded from the client", id)
		}
		return nil, err
	}

	return caller, nil
}

type gitSourceHandler struct {
	*gitSource
	src      source.GitIdentifier
	cacheKey string
	sm       *session.Manager
}

func (gs *gitSource) Resolve(ctx context.Context, id source.Identifier, sm *session.Manager) (source.SourceInstance, error) {
	gitIdentifier, ok := id.(*source.GitIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid git identifier %v", id)
	}

	return &gitSourceHandler{
		src:       *gitIdentifier,
		gitSource: gs,
		sm:        sm,
	}, nil
}

func (gs *gitSourceHandler) CacheKey(ctx context.Context, index int) (string, bool, error) {
	remote := gs.src.Remote
	ref := gs.src.Ref
	if ref == "" {
		ref = "master"
	}
	gs.locker.Lock(remote)
	defer gs.locker.Unlock(remote)

	if isCommitSHA(ref) {
		gs.cacheKey = ref
		return ref, true, nil
	}

	_, repo, unmountGitDir, err := gs.mountRemote(ctx, remote)
	if err != nil {
		return "", false, err
	}
	defer unmountGitDir()

	// TODO: should we assume that remote tag is immutable? add a timer?

	r, err := repo.Remote("origin")
	if err != nil {
		return "", false, errors.Wrap(err, "failed to get origin remote")
	}

	user, isSSH := parseSSHUser(remote)
	var getAgent func() (agent.Agent, error)
	if isSSH {
		c, err := getSSHCaller(ctx, gs.sm)
		if err != nil {
			return "", false, err
		}
		getAgent = sshforward.SSHAgentCallback(ctx, c, "default")
	}

	lo := &git.ListOptions{}
	if isSSH {
		lo.Auth = agentCallback(user, getAgent)
	}

	refs, err := r.List(lo)
	if err != nil {
		return "", false, errors.WithStack(err)
	}

	var h plumbing.Hash
	for _, r := range refs {
		if r.Name().Short() == ref {
			h = r.Hash()
			break
		}
	}
	if h == plumbing.ZeroHash {
		return "", false, errors.Errorf("failed to fetch %s from remote %s", ref, remote)
	}

	sha := h.String()
	if !isCommitSHA(sha) {
		return "", false, errors.Errorf("invalid commit sha %q", sha)
	}
	gs.cacheKey = sha
	return sha, true, nil
}

func (gs *gitSourceHandler) Snapshot(ctx context.Context) (out cache.ImmutableRef, retErr error) {
	ref := gs.src.Ref
	if ref == "" {
		ref = "master"
	}

	stdout, stderr := logs.NewLogStreams(ctx, false)
	defer stdout.Close()
	defer stderr.Close()

	cacheKey := gs.cacheKey
	if cacheKey == "" {
		var err error
		cacheKey, _, err = gs.CacheKey(ctx, 0)
		if err != nil {
			return nil, err
		}
	}

	snapshotKey := "git-snapshot::" + cacheKey + ":" + gs.src.Subdir
	gs.locker.Lock(snapshotKey)
	defer gs.locker.Unlock(snapshotKey)

	sis, err := gs.md.Search(snapshotKey)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search metadata for %s", snapshotKey)
	}
	if len(sis) > 0 {
		return gs.cache.Get(ctx, sis[0].ID())
	}

	gs.locker.Lock(gs.src.Remote)
	defer gs.locker.Unlock(gs.src.Remote)
	gitDir, repo, unmountGitDir, err := gs.mountRemote(ctx, gs.src.Remote)
	if err != nil {
		return nil, err
	}
	defer unmountGitDir()

	user, isSSH := parseSSHUser(gs.src.Remote)
	var getAgent func() (agent.Agent, error)
	if isSSH {
		c, err := getSSHCaller(ctx, gs.sm)
		if err != nil {
			return nil, err
		}
		getAgent = sshforward.SSHAgentCallback(ctx, c, "default")
	}

	doFetch := true
	if isCommitSHA(ref) {
		// skip fetch if commit already exists
		if _, err := repo.CommitObject(plumbing.NewHash(ref)); err == nil {
			doFetch = false
		}
	}

	if doFetch {
		// make sure no old lock files have leaked
		// os.RemoveAll(filepath.Join(gitDir, "shallow.lock"))
		var err error
		for _, t := range []string{"heads", "tags"} { // refs/*/<ref> do not pass tests for some reason
			fo := &git.FetchOptions{
				Progress: stderr,
			}

			if isSSH {
				fo.Auth = agentCallback(user, getAgent)
			}

			if !isCommitSHA(ref) { // TODO: find a branch from ls-remote?
				fo.Tags = git.NoTags
				fo.Depth = 1
				rs := config.RefSpec("refs/" + t + "/" + ref + ":refs/" + t + "/" + ref)
				if strings.HasPrefix(ref, "refs/") {
					rs = config.RefSpec(ref + ":" + ref)
				}
				fo.RefSpecs = []config.RefSpec{
					rs,
				}
				// local refs are needed so they would be advertised on next fetches
				// TODO: is there a better way to do this?
			}

			err = errors.WithStack(repo.FetchContext(ctx, fo))
			if errors.Cause(err) == git.NoErrAlreadyUpToDate {
				err = nil
			}
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	checkoutRef, err := gs.cache.New(ctx, nil, cache.WithRecordType(client.UsageRecordTypeGitCheckout), cache.WithDescription(fmt.Sprintf("git snapshot for %s#%s", gs.src.Remote, ref)))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create new mutable for %s", gs.src.Remote)
	}

	defer func() {
		if retErr != nil && checkoutRef != nil {
			checkoutRef.Release(context.TODO())
		}
	}()

	mount, err := checkoutRef.Mount(ctx, false)
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

	var wt *git.Worktree

	if gs.src.KeepGitDir {
		repo2, err := git.PlainInit(checkoutDir, false)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		r, err := repo2.CreateRemoteAnonymous(&config.RemoteConfig{
			Name: "anonymous",
			URLs: []string{gitDir},
		})
		if err != nil {
			return nil, errors.WithStack(err)
		}

		rs := config.RefSpec("refs/*/" + ref + ":refs/*/" + ref)
		if strings.HasPrefix(ref, "refs/") {
			rs = config.RefSpec(rs + ":" + rs)
		}

		fo := &git.FetchOptions{
			Progress: stderr,
			RefSpecs: []config.RefSpec{rs},
		}

		if isCommitSHA(ref) {
			pullref := "refs/buildkit/" + identity.NewID()
			rname := plumbing.ReferenceName(pullref)
			href := plumbing.NewHashReference(rname, plumbing.NewHash(ref))
			if err = repo.Storer.SetReference(href); err != nil {
				return nil, err
			}
			fo.RefSpecs = []config.RefSpec{config.RefSpec(pullref + ":" + pullref)}
			ref = pullref
		}

		if err := r.Fetch(fo); err != nil {
			if errors.Cause(err) != git.NoErrAlreadyUpToDate {
				return nil, errors.WithStack(err)
			}
		}

		wt, err = repo2.Worktree()
		if err != nil {
			return nil, errors.WithStack(err)
		}

		var h *plumbing.Hash
		if isCommitSHA(ref) {
			hh := plumbing.NewHash(ref)
			h = &hh
		} else {
			h, err = repo.ResolveRevision(plumbing.Revision(ref))
			if err != nil {
				return nil, errors.WithStack(err)
			}
		}

		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:  *h,
			Force: true,
		}); err != nil {
			return nil, errors.WithStack(err)
		}
	} else {
		fs := osfs.New(checkoutDir)
		repo2, err := git.Open(repo.Storer, fs)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		var h *plumbing.Hash
		if isCommitSHA(ref) {
			hh := plumbing.NewHash(ref)
			h = &hh
		} else {
			h, err = repo.ResolveRevision(plumbing.Revision(ref))
			if err != nil {
				return nil, errors.WithStack(err)
			}
		}

		wt, err = repo2.Worktree()
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:  *h,
			Force: true,
		}); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	submodules, err := wt.Submodules()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	for _, sm := range submodules {
		if err := sm.Update(&git.SubmoduleUpdateOptions{
			Init:              true,
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		}); err != nil {
			return nil, errors.WithStack(err)
		}
	}

	if idmap := mount.IdentityMapping(); idmap != nil {
		u := idmap.RootPair()
		err := filepath.Walk(gitDir, func(p string, f os.FileInfo, err error) error {
			return os.Lchown(p, u.UID, u.GID)
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
			snap.Release(context.TODO())
		}
	}()

	si, _ := gs.md.Get(snap.ID())
	v, err := metadata.NewValue(snapshotKey)
	v.Index = snapshotKey
	if err != nil {
		return nil, err
	}
	if err := si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, "git-snapshot", v)
	}); err != nil {
		return nil, err
	}

	return snap, nil
}

func isCommitSHA(str string) bool {
	return validHex.MatchString(str)
}

type PublicKeysCallback struct {
	User     string
	Callback func() (signers []ssh.Signer, err error)
}

func (a *PublicKeysCallback) Name() string {
	return gitssh.PublicKeysCallbackName
}

func (a *PublicKeysCallback) String() string {
	return fmt.Sprintf("name: %s", a.Name())
}

func (a *PublicKeysCallback) ClientConfig() (*ssh.ClientConfig, error) {
	return &ssh.ClientConfig{
		User: a.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeysCallback(a.Callback)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			// logrus.Debugf("HostKeyCallback %s %v %v", hostname, remote, key)
			return nil
		},
	}, nil
}

func parseSSHUser(url string) (string, bool) {
	if matchesScheme(url) || !matchesScpLike(url) {
		return "", false
	}

	user, _, _, _ := findScpLikeComponents(url)
	if user == "" {
		user = "root"
	}
	return user, true
}

func agentCallback(user string, f func() (agent.Agent, error)) *PublicKeysCallback {
	return &PublicKeysCallback{
		User: user,
		Callback: func() (signers []ssh.Signer, err error) {
			a, err := f()
			if err != nil {
				return nil, errors.WithStack(err)
			}
			return a.Signers()
		},
	}
}

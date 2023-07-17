package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/frontend/gateway/container"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/solver/llbsolver/mounts"
	"github.com/moby/buildkit/solver/llbsolver/ops/opsutils"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	utilsystem "github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/semaphore"
)

const execCacheType = "buildkit.exec.v0"

type execOp struct {
	op        *pb.ExecOp
	cm        cache.Manager
	sm        *session.Manager
	md        *metadata.Store
	exec      executor.Executor
	w         worker.Worker
	platform  *pb.Platform
	numInputs int

	cacheMounts map[string]*cacheRefShare
}

var _ solver.Op = &ExecOp{}

func NewExecOp(v solver.Vertex, op *pb.Op_Exec, platform *pb.Platform, cm cache.Manager, parallelism *semaphore.Weighted, sm *session.Manager, exec executor.Executor, w worker.Worker) (*ExecOp, error) {
	if err := opsutils.Validate(&pb.Op{Op: op}); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("exec %s", strings.Join(op.Exec.Meta.Args, " "))
	return &ExecOp{
		op:          op.Exec,
		mm:          mounts.NewMountManager(name, cm, sm),
		cm:          cm,
		sm:          sm,
		exec:        exec,
		numInputs:   len(v.Inputs()),
		w:           w,
		platform:    platform,
		parallelism: parallelism,
		digest:      v.Digest(),
	}, nil
}

func (e *ExecOp) Digest() digest.Digest {
	return e.digest
}

func (e *ExecOp) Proto() *pb.ExecOp {
	return e.op
}

func cloneExecOp(old *pb.ExecOp) pb.ExecOp {
	n := *old
	meta := *n.Meta
	meta.ExtraHosts = nil
	for i := range n.Meta.ExtraHosts {
		h := *n.Meta.ExtraHosts[i]
		meta.ExtraHosts = append(meta.ExtraHosts, &h)
	}
	n.Meta = &meta
	n.Mounts = nil
	for i := range old.Mounts {
		m := *old.Mounts[i]
		n.Mounts = append(n.Mounts, &m)
	}
	return n
}

func (e *ExecOp) CacheMap(ctx context.Context, g session.Group, index int) (*solver.CacheMap, bool, error) {
	op := cloneExecOp(e.op)
	for i := range op.Meta.ExtraHosts {
		h := op.Meta.ExtraHosts[i]
		h.IP = ""
		op.Meta.ExtraHosts[i] = h
	}
	for i := range op.Mounts {
		op.Mounts[i].Selector = ""
	}
	op.Meta.ProxyEnv = nil

	p := platforms.DefaultSpec()
	if e.platform != nil {
		p = ocispecs.Platform{
			OS:           e.platform.OS,
			Architecture: e.platform.Architecture,
			Variant:      e.platform.Variant,
		}
	}

	// Special case for cache compatibility with buggy versions that wrongly
	// excluded Exec.Mounts: for the default case of one root mount (i.e. RUN
	// inside a Dockerfile), do not include the mount when generating the cache
	// map.
	if len(op.Mounts) == 1 &&
		op.Mounts[0].Dest == "/" &&
		op.Mounts[0].Selector == "" &&
		!op.Mounts[0].Readonly &&
		op.Mounts[0].MountType == pb.MountType_BIND &&
		op.Mounts[0].CacheOpt == nil &&
		op.Mounts[0].SSHOpt == nil &&
		op.Mounts[0].SecretOpt == nil &&
		op.Mounts[0].ResultID == "" {
		op.Mounts = nil
	}

	dt, err := json.Marshal(struct {
		Type    string
		Exec    *pb.ExecOp
		OS      string
		Arch    string
		Variant string `json:",omitempty"`
	}{
		Type:    execCacheType,
		Exec:    &op,
		OS:      p.OS,
		Arch:    p.Architecture,
		Variant: p.Variant,
	})
	if err != nil {
		return nil, false, err
	}

	cm := &solver.CacheMap{
		Digest: digest.FromBytes(dt),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
			PreprocessFunc    solver.PreprocessFunc
		}, e.numInputs),
	}

	deps, err := e.getMountDeps()
	if err != nil {
		return nil, false, err
	}

	for i, dep := range deps {
		if len(dep.Selectors) != 0 {
			dgsts := make([][]byte, 0, len(dep.Selectors))
			for _, p := range dep.Selectors {
				dgsts = append(dgsts, []byte(p))
			}
			cm.Deps[i].Selector = digest.FromBytes(bytes.Join(dgsts, []byte{0}))
		}
		if !dep.NoContentBasedHash {
			cm.Deps[i].ComputeDigestFunc = opsutils.NewContentHashFunc(toSelectors(dedupePaths(dep.Selectors)))
		}
		cm.Deps[i].PreprocessFunc = unlazyResultFunc
	}

	return cm, true, nil
}

func dedupePaths(inp []string) []string {
	old := make(map[string]struct{}, len(inp))
	for _, p := range inp {
		old[p] = struct{}{}
	}
	paths := make([]string, 0, len(old))
	for p1 := range old {
		var skip bool
		for p2 := range old {
			if p1 != p2 && strings.HasPrefix(p1, p2+"/") {
				skip = true
				break
			}
		}
		if !skip {
			paths = append(paths, p1)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return paths[i] < paths[j]
	})
	return paths
}

func toSelectors(p []string) []opsutils.Selector {
	sel := make([]opsutils.Selector, 0, len(p))
	for _, p := range p {
		sel = append(sel, opsutils.Selector{Path: p, FollowLinks: true})
	}
	return sel
}

type dep struct {
	Selectors          []string
	NoContentBasedHash bool
}

func (e *ExecOp) getMountDeps() ([]dep, error) {
	deps := make([]dep, e.numInputs)
	for _, m := range e.op.Mounts {
		if m.Input == pb.Empty {
			continue
		}
		if int(m.Input) >= len(deps) {
			return nil, errors.Errorf("invalid mountinput %v", m)
		}

		sel := m.Selector
		if sel != "" {
			sel = path.Join("/", sel)
			deps[m.Input].Selectors = append(deps[m.Input].Selectors, sel)
		}

		if (!m.Readonly || m.Dest == pb.RootMount) && m.Output != -1 { // exclude read-only rootfs && read-write mounts
			deps[m.Input].NoContentBasedHash = true
		}
	}
	return deps, nil
}

func (e *execOp) getRefCacheDir(ctx context.Context, ref cache.ImmutableRef, id string, m *pb.Mount, sharing pb.CacheSharingOpt) (mref cache.MutableRef, err error) {
	key := "cache-dir:" + id
	if ref != nil {
		key += ":" + ref.ID()
	}
	mu := CacheMountsLocker()
	mu.Lock()
	defer mu.Unlock()

	if ref, ok := e.cacheMounts[key]; ok {
		return ref.clone(), nil
	}
	defer func() {
		if err == nil {
			share := &cacheRefShare{MutableRef: mref, refs: map[*cacheRef]struct{}{}}
			e.cacheMounts[key] = share
			mref = share.clone()
		}
	}()

	switch sharing {
	case pb.CacheSharingOpt_SHARED:
		return sharedCacheRefs.get(key, func() (cache.MutableRef, error) {
			return e.getRefCacheDirNoCache(ctx, key, ref, id, m, false)
		})
	case pb.CacheSharingOpt_PRIVATE:
		return e.getRefCacheDirNoCache(ctx, key, ref, id, m, false)
	case pb.CacheSharingOpt_LOCKED:
		return e.getRefCacheDirNoCache(ctx, key, ref, id, m, true)
	default:
		return nil, errors.Errorf("invalid cache sharing option: %s", sharing.String())
	}

}

func (e *execOp) getRefCacheDirNoCache(ctx context.Context, key string, ref cache.ImmutableRef, id string, m *pb.Mount, block bool) (cache.MutableRef, error) {
	makeMutable := func(ref cache.ImmutableRef) (cache.MutableRef, error) {
		desc := fmt.Sprintf("cached mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " "))
		return e.cm.New(ctx, ref, cache.WithRecordType(client.UsageRecordTypeCacheMount), cache.WithDescription(desc), cache.CachePolicyRetain)
	}

	cacheRefsLocker.Lock(key)
	defer cacheRefsLocker.Unlock(key)
	for {
		sis, err := e.md.Search(key)
		if err != nil {
			return nil, err
		}
		locked := false
		for _, si := range sis {
			if mRef, err := e.cm.GetMutable(ctx, si.ID()); err == nil {
				logrus.Debugf("reusing ref for cache dir: %s", mRef.ID())
				return mRef, nil
			} else if errors.Cause(err) == cache.ErrLocked {
				locked = true
			}
		}
		if block && locked {
			cacheRefsLocker.Unlock(key)
			select {
			case <-ctx.Done():
				cacheRefsLocker.Lock(key)
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
				cacheRefsLocker.Lock(key)
			}
		} else {
			break
		}
	}
	mRef, err := makeMutable(ref)
	if err != nil {
		return nil, err
	}

	si, _ := e.md.Get(mRef.ID())
	v, err := metadata.NewValue(key)
	if err != nil {
		mRef.Release(context.TODO())
		return nil, err
	}
	v.Index = key
	if err := si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, key, v)
	}); err != nil {
		mRef.Release(context.TODO())
		return nil, err
	}
	return mRef, nil
}

func (e *execOp) getSSHMountable(ctx context.Context, m *pb.Mount) (cache.Mountable, error) {
	sessionID := session.FromContext(ctx)
	if sessionID == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.sm.Get(timeoutCtx, sessionID)
	if err != nil {
		return nil, err
	}

	if err := sshforward.CheckSSHID(ctx, caller, m.SSHOpt.ID); err != nil {
		if m.SSHOpt.Optional {
			return nil, nil
		}
		if st, ok := status.FromError(errors.Cause(err)); ok && st.Code() == codes.Unimplemented {
			return nil, errors.Errorf("no SSH key %q forwarded from the client", m.SSHOpt.ID)
		}
		return nil, err
	}

	return &sshMount{mount: m, caller: caller, idmap: e.cm.IdentityMapping()}, nil
}

type sshMount struct {
	mount  *pb.Mount
	caller session.Caller
	idmap  *idtools.IdentityMapping
}

func (sm *sshMount) Mount(ctx context.Context, readonly bool) (snapshot.Mountable, error) {
	return &sshMountInstance{sm: sm, idmap: sm.idmap}, nil
}

type sshMountInstance struct {
	sm    *sshMount
	idmap *idtools.IdentityMapping
}

func (sm *sshMountInstance) Mount() ([]mount.Mount, func() error, error) {
	ctx, cancel := context.WithCancel(context.TODO())

	uid := int(sm.sm.mount.SSHOpt.Uid)
	gid := int(sm.sm.mount.SSHOpt.Gid)

	if sm.idmap != nil {
		identity, err := sm.idmap.ToHost(idtools.Identity{
			UID: uid,
			GID: gid,
		})
		if err != nil {
			return nil, nil, err
		}
		uid = identity.UID
		gid = identity.GID
	}

	sock, cleanup, err := sshforward.MountSSHSocket(ctx, sm.sm.caller, sshforward.SocketOpt{
		ID:   sm.sm.mount.SSHOpt.ID,
		UID:  uid,
		GID:  gid,
		Mode: int(sm.sm.mount.SSHOpt.Mode & 0777),
	})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	release := func() error {
		var err error
		if cleanup != nil {
			err = cleanup()
		}
		cancel()
		return err
	}

	return []mount.Mount{{
		Type:    "bind",
		Source:  sock,
		Options: []string{"rbind"},
	}}, release, nil
}

func (sm *sshMountInstance) IdentityMapping() *idtools.IdentityMapping {
	return sm.idmap
}

func (e *execOp) getSecretMountable(ctx context.Context, m *pb.Mount) (cache.Mountable, error) {
	if m.SecretOpt == nil {
		return nil, errors.Errorf("invalid sercet mount options")
	}
	sopt := *m.SecretOpt

	id := sopt.ID
	if id == "" {
		return nil, errors.Errorf("secret ID missing from mount options")
	}

	sessionID := session.FromContext(ctx)
	if sessionID == "" {
		return nil, errors.New("could not access local files without session")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.sm.Get(timeoutCtx, sessionID)
	if err != nil {
		return nil, err
	}

	dt, err := secrets.GetSecret(ctx, caller, id)
	if err != nil {
		if errors.Cause(err) == secrets.ErrNotFound && m.SecretOpt.Optional {
			return nil, nil
		}
		return nil, err
	}

	return &secretMount{mount: m, data: dt, idmap: e.cm.IdentityMapping()}, nil
}

type secretMount struct {
	mount *pb.Mount
	data  []byte
	idmap *idtools.IdentityMapping
}

func (sm *secretMount) Mount(ctx context.Context, readonly bool) (snapshot.Mountable, error) {
	return &secretMountInstance{sm: sm, idmap: sm.idmap}, nil
}

type secretMountInstance struct {
	sm    *secretMount
	root  string
	idmap *idtools.IdentityMapping
}

func (sm *secretMountInstance) Mount() ([]mount.Mount, func() error, error) {
	dir, err := ioutil.TempDir("", "buildkit-secrets")
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create temp dir")
	}
	cleanupDir := func() error {
		return os.RemoveAll(dir)
	}

	if err := os.Chmod(dir, 0711); err != nil {
		cleanupDir()
		return nil, nil, err
	}

	tmpMount := mount.Mount{
		Type:    "tmpfs",
		Source:  "tmpfs",
		Options: []string{"nodev", "nosuid", "noexec", fmt.Sprintf("uid=%d,gid=%d", os.Geteuid(), os.Getegid())},
	}

	if system.RunningInUserNS() {
		tmpMount.Options = nil
	}

	if err := mount.All([]mount.Mount{tmpMount}, dir); err != nil {
		cleanupDir()
		return nil, nil, errors.Wrap(err, "unable to setup secret mount")
	}
	sm.root = dir

	cleanup := func() error {
		if err := mount.Unmount(dir, 0); err != nil {
			return err
		}
		return cleanupDir()
	}

	randID := identity.NewID()
	fp := filepath.Join(dir, randID)
	if err := ioutil.WriteFile(fp, sm.sm.data, 0600); err != nil {
		cleanup()
		return nil, nil, err
	}

	uid := int(sm.sm.mount.SecretOpt.Uid)
	gid := int(sm.sm.mount.SecretOpt.Gid)

	if sm.idmap != nil {
		identity, err := sm.idmap.ToHost(idtools.Identity{
			UID: uid,
			GID: gid,
		})
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		uid = identity.UID
		gid = identity.GID
	}

	if err := os.Chown(fp, uid, gid); err != nil {
		cleanup()
		return nil, nil, err
	}

	if err := os.Chmod(fp, os.FileMode(sm.sm.mount.SecretOpt.Mode&0777)); err != nil {
		cleanup()
		return nil, nil, err
	}

	return []mount.Mount{{
		Type:    "bind",
		Source:  fp,
		Options: []string{"ro", "rbind"},
	}}, cleanup, nil
}

func (sm *secretMountInstance) IdentityMapping() *idtools.IdentityMapping {
	return sm.idmap
}

func addDefaultEnvvar(env []string, k, v string) []string {
	for _, e := range env {
		if strings.HasPrefix(e, k+"=") {
			return env
		}
	}
	return append(env, k+"="+v)
}

func (e *ExecOp) Exec(ctx context.Context, g session.Group, inputs []solver.Result) (results []solver.Result, err error) {
	trace.SpanFromContext(ctx).AddEvent("ExecOp started")

	refs := make([]*worker.WorkerRef, len(inputs))
	for i, inp := range inputs {
		var ok bool
		refs[i], ok = inp.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, errors.Errorf("invalid reference for exec %T", inp.Sys())
		}
	}

	platformOS := runtime.GOOS
	if e.platform != nil {
		platformOS = e.platform.OS
	}
	p, err := container.PrepareMounts(ctx, e.mm, e.cm, g, e.op.Meta.Cwd, e.op.Mounts, refs, func(m *pb.Mount, ref cache.ImmutableRef) (cache.MutableRef, error) {
		desc := fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " "))
		return e.cm.New(ctx, ref, g, cache.WithDescription(desc))
	}, platformOS)
	defer func() {
		if err != nil {
			execInputs := make([]solver.Result, len(e.op.Mounts))
			for i, m := range e.op.Mounts {
				if m.Input == -1 {
					continue
				}
				execInputs[i] = inputs[m.Input].Clone()
			}
			execMounts := make([]solver.Result, len(e.op.Mounts))
			copy(execMounts, execInputs)
			for i, res := range results {
				execMounts[p.OutputRefs[i].MountIndex] = res
			}
			for _, active := range p.Actives {
				if active.NoCommit {
					active.Ref.Release(context.TODO())
				} else {
					ref, cerr := active.Ref.Commit(ctx)
					if cerr != nil {
						err = errors.Wrapf(err, "error committing %s: %s", active.Ref.ID(), cerr)
						continue
					}
					execMounts[active.MountIndex] = worker.NewWorkerRefResult(ref, e.w)
				}
			}
			err = errdefs.WithExecError(err, execInputs, execMounts)
		} else {
			// Only release actives if err is nil.
			for i := len(p.Actives) - 1; i >= 0; i-- { // call in LIFO order
				p.Actives[i].Ref.Release(context.TODO())
			}
		}
		for _, o := range p.OutputRefs {
			if o.Ref != nil {
				o.Ref.Release(context.TODO())
			}
		}
	}()
	if err != nil {
		return nil, err
	}

	extraHosts, err := container.ParseExtraHosts(e.op.Meta.ExtraHosts)
	if err != nil {
		return nil, err
	}

	emu, err := getEmulator(ctx, e.platform, e.cm.IdentityMapping())
	if err != nil {
		return nil, err
	}
	if emu != nil {
		e.op.Meta.Args = append([]string{qemuMountName}, e.op.Meta.Args...)

		p.Mounts = append(p.Mounts, executor.Mount{
			Readonly: true,
			Src:      emu,
			Dest:     qemuMountName,
		})
	}

	meta := executor.Meta{
		Args:                      e.op.Meta.Args,
		Env:                       e.op.Meta.Env,
		Cwd:                       e.op.Meta.Cwd,
		User:                      e.op.Meta.User,
		Hostname:                  e.op.Meta.Hostname,
		ReadonlyRootFS:            p.ReadonlyRootFS,
		ExtraHosts:                extraHosts,
		Ulimit:                    e.op.Meta.Ulimit,
		CgroupParent:              e.op.Meta.CgroupParent,
		NetMode:                   e.op.Network,
		SecurityMode:              e.op.Security,
		RemoveMountStubsRecursive: e.op.Meta.RemoveMountStubsRecursive,
	}

	if e.op.Meta.ProxyEnv != nil {
		meta.Env = append(meta.Env, proxyEnvList(e.op.Meta.ProxyEnv)...)
	}
	var currentOS string
	if e.platform != nil {
		currentOS = e.platform.OS
	}
	meta.Env = addDefaultEnvvar(meta.Env, "PATH", utilsystem.DefaultPathEnv(currentOS))

	secretEnv, err := e.loadSecretEnv(ctx, g)
	if err != nil {
		return nil, err
	}
	meta.Env = append(meta.Env, secretEnv...)

	stdout, stderr, flush := logs.NewLogStreams(ctx, os.Getenv("BUILDKIT_DEBUG_EXEC_OUTPUT") == "1")
	defer stdout.Close()
	defer stderr.Close()
	defer func() {
		if err != nil {
			flush()
		}
	}()

	rec, execErr := e.exec.Run(ctx, "", p.Root, p.Mounts, executor.ProcessInfo{
		Meta:   meta,
		Stdin:  nil,
		Stdout: stdout,
		Stderr: stderr,
	}, nil)

	for i, out := range p.OutputRefs {
		if mutable, ok := out.Ref.(cache.MutableRef); ok {
			ref, err := mutable.Commit(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "error committing %s", mutable.ID())
			}
			results = append(results, worker.NewWorkerRefResult(ref, e.w))
		} else {
			results = append(results, worker.NewWorkerRefResult(out.Ref.(cache.ImmutableRef), e.w))
		}
		// Prevent the result from being released.
		p.OutputRefs[i].Ref = nil
	}
	e.rec = rec
	return results, errors.Wrapf(execErr, "process %q did not complete successfully", strings.Join(e.op.Meta.Args, " "))
}

func proxyEnvList(p *pb.ProxyEnv) []string {
	out := []string{}
	if v := p.HttpProxy; v != "" {
		out = append(out, "HTTP_PROXY="+v, "http_proxy="+v)
	}
	if v := p.HttpsProxy; v != "" {
		out = append(out, "HTTPS_PROXY="+v, "https_proxy="+v)
	}
	if v := p.FtpProxy; v != "" {
		out = append(out, "FTP_PROXY="+v, "ftp_proxy="+v)
	}
	if v := p.NoProxy; v != "" {
		out = append(out, "NO_PROXY="+v, "no_proxy="+v)
	}
	if v := p.AllProxy; v != "" {
		out = append(out, "ALL_PROXY="+v, "all_proxy="+v)
	}
	return out
}

func (e *ExecOp) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	if e.parallelism == nil {
		return func() {}, nil
	}
	return []mount.Mount{{
		Type:    "tmpfs",
		Source:  "tmpfs",
		Options: opt,
	}}, func() error { return nil }, nil
}

func (m *tmpfsMount) IdentityMapping() *idtools.IdentityMapping {
	return m.idmap
}

var cacheRefsLocker = locker.New()
var sharedCacheRefs = &cacheRefs{}

type cacheRefs struct {
	mu     sync.Mutex
	shares map[string]*cacheRefShare
}

// ClearActiveCacheMounts clears shared cache mounts currently in use.
// Caller needs to hold CacheMountsLocker before calling
func ClearActiveCacheMounts() {
	sharedCacheRefs.shares = nil
}

func CacheMountsLocker() sync.Locker {
	return &sharedCacheRefs.mu
}

func (r *cacheRefs) get(key string, fn func() (cache.MutableRef, error)) (cache.MutableRef, error) {
	if r.shares == nil {
		r.shares = map[string]*cacheRefShare{}
	}

	share, ok := r.shares[key]
	if ok {
		return share.clone(), nil
	}

	mref, err := fn()
	if err != nil {
		return nil, err
	}

	share = &cacheRefShare{MutableRef: mref, main: r, key: key, refs: map[*cacheRef]struct{}{}}
	r.shares[key] = share

	return share.clone(), nil
}

type cacheRefShare struct {
	cache.MutableRef
	mu   sync.Mutex
	refs map[*cacheRef]struct{}
	main *cacheRefs
	key  string
}

func (r *cacheRefShare) clone() cache.MutableRef {
	cacheRef := &cacheRef{cacheRefShare: r}
	r.mu.Lock()
	r.refs[cacheRef] = struct{}{}
	r.mu.Unlock()
	return cacheRef
}

func (r *cacheRefShare) release(ctx context.Context) error {
	if r.main != nil {
		r.main.mu.Lock()
		defer r.main.mu.Unlock()
		delete(r.main.shares, r.key)
	}
	return r.MutableRef.Release(ctx)
}

type cacheRef struct {
	*cacheRefShare
}

func (r *cacheRef) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.refs, r)
	if len(r.refs) == 0 {
		return r.release(ctx)
	}
	return nil
}

func parseExtraHosts(ips []*pb.HostIP) ([]executor.HostIP, error) {
	out := make([]executor.HostIP, len(ips))
	for i, hip := range ips {
		ip := net.ParseIP(hip.IP)
		if ip == nil {
			return nil, errors.Errorf("failed to parse IP %s", hip.IP)
		}
		out = append(out, fmt.Sprintf("%s=%s", sopt.Name, string(dt)))
	}
	return out, nil
}

func (e *ExecOp) IsProvenanceProvider() {
}

func (e *ExecOp) Samples() (*resourcestypes.Samples, error) {
	if e.rec == nil {
		return nil, nil
	}
	return e.rec.Samples()
}

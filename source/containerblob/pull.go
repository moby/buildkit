package containerblob

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/remotes"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type puller struct {
	src *Source
	id  *ImageBlobIdentifier

	SessionManager *session.Manager

	rc   io.ReadCloser
	dgst digest.Digest
}

func (p *puller) hash() (digest.Digest, error) {
	dgst := p.id.Reference.Digest()
	if err := dgst.Validate(); err != nil {
		return "", err
	}

	dt, err := json.Marshal(struct {
		Digest         digest.Digest
		Filename       string
		Perm, UID, GID int
	}{
		Digest:   dgst,
		Filename: p.id.Filename,
		Perm:     p.id.Perm,
		UID:      p.id.UID,
		GID:      p.id.GID,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func (p *puller) ensureResolver(ctx context.Context, g session.Group) error {
	if p.rc != nil {
		return nil
	}
	dgst := p.id.Reference.Digest()
	if err := dgst.Validate(); err != nil {
		return errors.Wrapf(err, "invalid reference digest")
	}

	resolver := resolver.DefaultPool.GetResolver(p.src.RegistryHosts, p.id.Reference.String(), "pull", p.SessionManager, g)

	f, err := resolver.Fetcher(ctx, p.id.Reference.String())
	if err != nil {
		return err
	}

	fd, ok := f.(remotes.FetcherByDigest)
	if !ok {
		return errors.Errorf("invalid blob fetcher: %T", f)
	}

	rc, _, err := fd.FetchByDigest(ctx, dgst)
	if err != nil {
		return err
	}

	p.rc = rc
	p.dgst = dgst
	return nil
}

func (p *puller) CacheKey(ctx context.Context, g session.Group, index int) (cacheKey string, imgDigest string, cacheOpts solver.CacheOpts, cacheDone bool, err error) {
	dgst := p.id.Reference.Digest()
	if err := dgst.Validate(); err != nil {
		return "", "", nil, false, errors.Wrapf(err, "invalid reference digest")
	}

	info, err := p.src.ContentStore.Info(ctx, dgst)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return "", "", nil, false, err
		}
	}

	if ok, err := contentutil.HasSource(info, p.id.Reference); err == nil && ok {
		h, err := p.hash()
		if err != nil {
			return "", "", nil, false, err
		}
		return h.String(), dgst.String(), nil, true, nil
	}

	h, err := p.hash()
	if err != nil {
		return "", "", nil, false, err
	}
	return h.String(), dgst.String(), nil, true, nil
}

func (p *puller) Snapshot(ctx context.Context, g session.Group) (ir cache.ImmutableRef, err error) {
	if err := p.ensureResolver(ctx, g); err != nil {
		return nil, err
	}

	newRef, err := p.src.CacheAccessor.New(ctx, nil, g, cache.CachePolicyRetain, cache.WithDescription(fmt.Sprintf("blob %s", p.id.Reference.String())))
	if err != nil {
		return nil, err
	}

	releaseRef := func() {
		newRef.Release(context.TODO())
	}

	defer func() {
		if err != nil && newRef != nil {
			releaseRef()
		}
	}()

	mount, err := newRef.Mount(ctx, false, g)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil && lm != nil {
			lm.Unmount()
		}
	}()
	perm := 0600
	if p.id.Perm != 0 {
		perm = p.id.Perm
	}
	fn := p.id.Filename
	if fn == "" {
		fn = p.dgst.Hex()
	}

	fp := filepath.Join(dir, fn)

	f, err := os.OpenFile(fp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(perm))
	if err != nil {
		return nil, err
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	h := sha256.New()

	if _, err := io.Copy(io.MultiWriter(f, h), p.rc); err != nil {
		return nil, err
	}

	if err := f.Close(); err != nil {
		return nil, err
	}
	f = nil

	uid := p.id.UID
	gid := p.id.GID
	if idmap := mount.IdentityMapping(); idmap != nil {
		identity, err := idmap.ToHost(idtools.Identity{
			UID: int(uid),
			GID: int(gid),
		})
		if err != nil {
			return nil, err
		}
		uid = identity.UID
		gid = identity.GID
	}

	if gid != 0 || uid != 0 {
		if err := os.Chown(fp, uid, gid); err != nil {
			return nil, err
		}
	}

	mTime := time.Unix(0, 0)
	// TODO: does mtime need to be configurable

	if err := os.Chtimes(fp, mTime, mTime); err != nil {
		return nil, err
	}

	lm.Unmount()
	lm = nil

	ref, err := newRef.Commit(ctx)
	if err != nil {
		return nil, err
	}
	newRef = nil
	return ref, nil
}

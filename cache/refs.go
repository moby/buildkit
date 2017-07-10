package cache

import (
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type ImmutableRef interface {
	Mountable
	ID() string
	Release(context.Context) error
	Size(ctx context.Context) (int64, error)
	Parent() ImmutableRef
	// Prepare() / ChainID() / Meta()
}

type MutableRef interface {
	Mountable
	ID() string
	Freeze() (ImmutableRef, error)
	ReleaseAndCommit(ctx context.Context) (ImmutableRef, error)
	Size(ctx context.Context) (int64, error)
}

type Mountable interface {
	Mount(ctx context.Context) ([]mount.Mount, error)
}

type cacheRecord struct {
	mu      sync.Mutex
	mutable bool
	frozen  bool
	// meta   SnapMeta
	refs      map[Mountable]struct{}
	cm        *cacheManager
	parent    ImmutableRef
	md        *metadata.StorageItem
	view      string
	viewMount []mount.Mount

	sizeG flightcontrol.Group
	// size  int64
}

// hold manager lock before calling
func (cr *cacheRecord) ref() *immutableRef {
	ref := &immutableRef{cacheRecord: cr}
	cr.refs[ref] = struct{}{}
	return ref
}

// hold manager lock before calling
func (cr *cacheRecord) mref() *mutableRef {
	ref := &mutableRef{cacheRecord: cr}
	cr.refs[ref] = struct{}{}
	return ref
}

func (cr *cacheRecord) Size(ctx context.Context) (int64, error) {
	// this expects that usage() is implemented lazily
	s, err := cr.sizeG.Do(ctx, cr.ID(), func(ctx context.Context) (interface{}, error) {
		cr.mu.Lock()
		s := getSize(cr.md)
		cr.mu.Unlock()
		if s != sizeUnknown {
			return s, nil
		}
		usage, err := cr.cm.ManagerOpt.Snapshotter.Usage(ctx, cr.ID())
		if err != nil {
			return s, errors.Wrapf(err, "failed to get usage for %s", cr.ID())
		}
		cr.mu.Lock()
		setSize(cr.md, usage.Size)
		if err := cr.md.Commit(); err != nil {
			return s, err
		}
		cr.mu.Unlock()
		return usage.Size, nil
	})
	return s.(int64), err
}

func (cr *cacheRecord) Parent() ImmutableRef {
	if cr.parent == nil {
		return nil
	}
	return cr.parent.(*immutableRef).ref()
}

func (cr *cacheRecord) Mount(ctx context.Context) ([]mount.Mount, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.mutable {
		m, err := cr.cm.Snapshotter.Mounts(ctx, cr.ID())
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", cr.ID())
		}
		return m, nil
	}
	if cr.viewMount == nil { // TODO: handle this better
		cr.view = identity.NewID()
		m, err := cr.cm.Snapshotter.View(ctx, cr.view, cr.ID())
		if err != nil {
			cr.view = ""
			return nil, errors.Wrapf(err, "failed to mount %s", cr.ID())
		}
		cr.viewMount = m
	}
	return cr.viewMount, nil
}

func (cr *cacheRecord) ID() string {
	return cr.md.ID()
}

type immutableRef struct {
	*cacheRecord
}

type mutableRef struct {
	*cacheRecord
}

func (sr *immutableRef) Release(ctx context.Context) error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release(ctx)
}

func (sr *immutableRef) release(ctx context.Context) error {
	if sr.viewMount != nil {
		if err := sr.cm.Snapshotter.Remove(ctx, sr.view); err != nil {
			return err
		}
		sr.view = ""
		sr.viewMount = nil
	}

	delete(sr.refs, sr)
	sr.frozen = false

	if len(sr.refs) == 0 {
		//go sr.cm.GC()
	}

	return nil
}

func (sr *mutableRef) Freeze() (ImmutableRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.mutable || sr.frozen || len(sr.refs) != 1 {
		return nil, errors.Wrapf(errInvalid, "invalid mutable")
	}

	if _, ok := sr.refs[sr]; !ok {
		return nil, errors.Wrapf(errInvalid, "invalid mutable")
	}

	delete(sr.refs, sr)

	sri := sr.ref()

	sri.frozen = true
	setSize(sr.md, sizeUnknown)
	if err := sr.md.Commit(); err != nil {
		return nil, err
	}

	return sri, nil
}

func (sr *mutableRef) ReleaseAndCommit(ctx context.Context) (ImmutableRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()

	if !sr.mutable || sr.frozen {
		sr.mu.Unlock()
		return nil, errors.Wrapf(errInvalid, "invalid mutable")
	}
	if len(sr.refs) != 1 {
		sr.mu.Unlock()
		return nil, errors.Wrapf(errInvalid, "multiple mutable references")
	}

	sr.mu.Unlock()

	id := identity.NewID()

	err := sr.cm.Snapshotter.Commit(ctx, id, sr.ID())
	if err != nil {
		return nil, errors.Wrapf(err, "failed to commit %s", sr.ID())
	}

	delete(sr.cm.records, sr.ID())

	if err := sr.cm.md.Clear(sr.ID()); err != nil {
		return nil, err
	}

	md, _ := sr.cm.md.Get(id)

	rec := &cacheRecord{
		cm:     sr.cm,
		parent: sr.parent,
		refs:   make(map[Mountable]struct{}),
		md:     &md,
	}
	sr.cm.records[id] = rec // TODO: save to db

	return rec.ref(), nil
}

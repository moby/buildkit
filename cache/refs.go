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
	Finalize(ctx context.Context) error // Make sure reference is flushed to driver
	// Prepare() / ChainID() / Meta()
}

type MutableRef interface {
	Mountable
	ID() string
	Commit(context.Context) (ImmutableRef, error)
	Release(context.Context) error
	Size(ctx context.Context) (int64, error)
}

type Mountable interface {
	Mount(ctx context.Context, readonly bool) ([]mount.Mount, error)
}

type cacheRecord struct {
	mu        sync.Mutex
	mutable   bool
	refs      map[Mountable]struct{}
	cm        *cacheManager
	parent    ImmutableRef
	md        *metadata.StorageItem
	view      string
	viewMount []mount.Mount

	sizeG flightcontrol.Group
	// size  int64

	// these are filled if multiple refs point to same data
	equalMutable   *mutableRef
	equalImmutable *immutableRef
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
		if s != sizeUnknown {
			cr.mu.Unlock()
			return s, nil
		}
		driverID := cr.ID()
		if cr.equalMutable != nil {
			driverID = cr.equalMutable.ID()
		}
		cr.mu.Unlock()
		usage, err := cr.cm.ManagerOpt.Snapshotter.Usage(ctx, driverID)
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

func (cr *cacheRecord) Mount(ctx context.Context, readonly bool) ([]mount.Mount, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.mutable {
		m, err := cr.cm.Snapshotter.Mounts(ctx, cr.ID())
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", cr.ID())
		}
		if readonly {
			m = setReadonly(m)
		}
		return m, nil
	}

	if cr.equalMutable != nil && readonly {
		m, err := cr.cm.Snapshotter.Mounts(ctx, cr.equalMutable.ID())
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", cr.equalMutable.ID())
		}
		return setReadonly(m), nil
	}

	if err := cr.finalize(ctx); err != nil {
		return nil, err
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

func (cr *cacheRecord) remove(ctx context.Context, removeSnapshot bool) error {
	if err := cr.cm.md.Clear(cr.ID()); err != nil {
		return err
	}
	if removeSnapshot {
		if err := cr.cm.Snapshotter.Remove(ctx, cr.ID()); err != nil {
			return err
		}
	}
	return nil
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

	if len(sr.refs) == 0 {
		if sr.equalMutable != nil {
			sr.equalMutable.release(ctx)
		}
		// go sr.cm.GC()
	}

	return nil
}

func (sr *immutableRef) Finalize(ctx context.Context) error {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.finalize(ctx)
}

func (sr *cacheRecord) finalize(ctx context.Context) error {
	mutable := sr.equalMutable
	if mutable == nil {
		return nil
	}
	err := sr.cm.Snapshotter.Commit(ctx, sr.ID(), sr.equalMutable.ID())
	if err != nil {
		return errors.Wrapf(err, "failed to commit %s", sr.equalMutable.ID())
	}
	delete(sr.cm.records, sr.equalMutable.ID())
	if err := sr.equalMutable.remove(ctx, false); err != nil {
		return err
	}
	sr.equalMutable = nil
	clearEqualMutable(sr.md)
	return sr.md.Commit()
}

func (sr *mutableRef) commit(ctx context.Context) (ImmutableRef, error) {
	if !sr.mutable || len(sr.refs) == 0 {
		return nil, errors.Wrapf(errInvalid, "invalid mutable")
	}

	id := identity.NewID()
	md, _ := sr.cm.md.Get(id)

	rec := &cacheRecord{
		cm:           sr.cm,
		parent:       sr.parent,
		equalMutable: sr,
		refs:         make(map[Mountable]struct{}),
		md:           &md,
	}

	sr.cm.records[id] = rec

	if err := sr.md.Commit(); err != nil {
		return nil, err
	}

	setSize(&md, sizeUnknown)
	setEqualMutable(&md, sr.ID())
	if err := md.Commit(); err != nil {
		return nil, err
	}

	ref := rec.ref()
	sr.equalImmutable = ref
	return ref, nil
}

func (sr *mutableRef) Commit(ctx context.Context) (ImmutableRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.commit(ctx)
}

func (sr *mutableRef) Release(ctx context.Context) error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release(ctx)
}

func (sr *mutableRef) release(ctx context.Context) error {
	delete(sr.refs, sr)
	// delete(sr.cm.records, sr.ID())
	// if err := sr.remove(ctx, true); err != nil {
	// 	return err
	// }
	return nil
}

func setReadonly(mounts []mount.Mount) []mount.Mount {
	for i, m := range mounts {
		opts := make([]string, 0, len(m.Options))
		for _, opt := range m.Options {
			if opt != "rw" {
				opts = append(opts, opt)
			}
		}
		opts = append(opts, "ro")
		mounts[i].Options = opts
	}
	return mounts
}

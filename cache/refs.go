package cache

import (
	"sync"
	"time"

	"github.com/containerd/containerd/mount"
	cdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

// Ref is a reference to cacheable objects.
type Ref interface {
	Mountable
	ID() string
	Release(context.Context) error
	Size(ctx context.Context) (int64, error)
	Metadata() *metadata.StorageItem
}

type ImmutableRef interface {
	Ref
	Parent() ImmutableRef
	Finalize(ctx context.Context) error // Make sure reference is flushed to driver
	// Prepare() / ChainID() / Meta()
}

type MutableRef interface {
	Ref
	Commit(context.Context) (ImmutableRef, error)
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
	dead      bool

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
		labels := map[string]string{
			"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339Nano),
		}
		m, err := cr.cm.Snapshotter.View(ctx, cr.view, cr.ID(), cdsnapshot.WithLabels(labels))
		if err != nil {
			cr.view = ""
			return nil, errors.Wrapf(err, "failed to mount %s", cr.ID())
		}
		cr.viewMount = m
	}
	return cr.viewMount, nil
}

func (cr *cacheRecord) remove(ctx context.Context, removeSnapshot bool) error {
	delete(cr.cm.records, cr.ID())
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

	updateLastUsed(sr.md)

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

func (cr *cacheRecord) Metadata() *metadata.StorageItem {
	return cr.md
}

func (cr *cacheRecord) finalize(ctx context.Context) error {
	mutable := cr.equalMutable
	if mutable == nil {
		return nil
	}
	err := cr.cm.Snapshotter.Commit(ctx, cr.ID(), mutable.ID())
	if err != nil {
		return errors.Wrapf(err, "failed to commit %s", mutable.ID())
	}
	mutable.dead = true
	go func() {
		cr.cm.mu.Lock()
		defer cr.cm.mu.Unlock()
		if err := mutable.remove(context.TODO(), false); err != nil {
			logrus.Error(err)
		}
	}()
	cr.equalMutable = nil
	clearEqualMutable(cr.md)
	return cr.md.Commit()
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
		md:           md,
	}

	if descr := GetDescription(sr.md); descr != "" {
		if err := queueDescription(md, descr); err != nil {
			return nil, err
		}
	}

	if err := initializeMetadata(rec); err != nil {
		return nil, err
	}

	sr.cm.records[id] = rec

	if err := sr.md.Commit(); err != nil {
		return nil, err
	}

	setSize(md, sizeUnknown)
	setEqualMutable(md, sr.ID())
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
	updateLastUsed(sr.md)
	if getCachePolicy(sr.md) != cachePolicyRetain {
		if sr.equalImmutable != nil {
			if getCachePolicy(sr.equalImmutable.md) == cachePolicyRetain {
				return nil
			}
			if err := sr.equalImmutable.remove(ctx, false); err != nil {
				return err
			}
		}
		if sr.parent != nil {
			if err := sr.parent.(*immutableRef).release(ctx); err != nil {
				return err
			}
		}
		return sr.remove(ctx, true)
	}
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

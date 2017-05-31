package cache

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/util/flightcontrol"
	"golang.org/x/net/context"
)

const sizeUnknown int64 = -1

type ImmutableRef interface {
	Mountable
	ID() string
	Release() error
	Size(ctx context.Context) (int64, error)
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
	Mount() ([]mount.Mount, error)
}

type cacheRecord struct {
	mu      sync.Mutex
	mutable bool
	frozen  bool
	// meta   SnapMeta
	refs      map[*cacheRef]struct{}
	id        string
	cm        *cacheManager
	parent    ImmutableRef
	view      string
	viewMount []mount.Mount

	sizeG flightcontrol.Group
	size  int64
}

// hold manager lock before calling
func (cr *cacheRecord) ref() *cacheRef {
	ref := &cacheRef{cacheRecord: cr}
	cr.refs[ref] = struct{}{}
	return ref
}

func (cr *cacheRecord) Size(ctx context.Context) (int64, error) {
	// this expects that usage() is implemented lazily
	s, err, _ := cr.sizeG.Do(ctx, cr.id, func(ctx context.Context) (interface{}, error) {
		cr.mu.Lock()
		s := cr.size
		cr.mu.Unlock()
		if s != sizeUnknown {
			return s, nil
		}
		usage, err := cr.cm.ManagerOpt.Snapshotter.Usage(ctx, cr.id)
		if err != nil {
			return s, errors.Wrapf(err, "failed to get usage for %s", cr.id)
		}
		cr.mu.Lock()
		cr.size = s
		cr.mu.Unlock()
		return usage.Size, nil
	})
	return s.(int64), err
}

type cacheRef struct {
	*cacheRecord
}

func (sr *cacheRef) Mount() ([]mount.Mount, error) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.mutable {
		m, err := sr.cm.Snapshotter.Mounts(context.TODO(), sr.id)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", sr.id)
		}
		return m, nil
	} else {
		if sr.viewMount == nil { // TODO: handle this better
			sr.view = generateID()
			m, err := sr.cm.Snapshotter.View(context.TODO(), sr.view, sr.id)
			if err != nil {
				sr.view = ""
				return nil, errors.Wrapf(err, "failed to mount %s", sr.id)
			}
			sr.viewMount = m
		}
		return sr.viewMount, nil
	}

	return nil, errors.New("snapshot mount not implemented")
}

func (sr *cacheRef) Release() error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release()
}

func (sr *cacheRef) release() error {
	if sr.parent != nil {
		if err := sr.parent.(*cacheRef).release(); err != nil {
			return err
		}
	}
	if sr.viewMount != nil {
		if err := sr.cm.Snapshotter.Remove(context.TODO(), sr.view); err != nil {
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

func (sr *cacheRef) Freeze() (ImmutableRef, error) {
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

	sr.frozen = true
	sr.size = sizeUnknown

	return sr, nil
}

func (sr *cacheRef) ReleaseAndCommit(ctx context.Context) (ImmutableRef, error) {
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

	id := generateID() // TODO: no need to actually switch the key here

	err := sr.cm.Snapshotter.Commit(ctx, id, sr.id)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to commit %s", sr.id)
	}

	delete(sr.cm.records, sr.id)

	rec := &cacheRecord{
		id:   id,
		cm:   sr.cm,
		refs: make(map[*cacheRef]struct{}),
		size: sizeUnknown,
	}
	sr.cm.records[id] = rec // TODO: save to db

	return rec.ref(), nil
}

func (sr *cacheRef) ID() string {
	return sr.id
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

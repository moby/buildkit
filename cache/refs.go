package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/pkg/errors"
)

type ImmutableRef interface {
	Mountable
	ID() string
	Release() error
	Size() (int64, error)
	// Prepare() / ChainID() / Meta()
}

type MutableRef interface {
	Mountable
	ID() string
	Freeze() (ImmutableRef, error)
	ReleaseAndCommit(ctx context.Context) (ImmutableRef, error)
	Size() (int64, error)
}

type Mountable interface {
	Mount() ([]mount.Mount, error)
}

type cacheRecord struct {
	mu      sync.Mutex
	mutable bool
	frozen  bool
	// meta   SnapMeta
	refs   map[*cacheRef]struct{}
	id     string
	cm     *cacheManager
	parent ImmutableRef
}

// hold manager lock before calling
func (cr *cacheRecord) ref() *cacheRef {
	ref := &cacheRef{cacheRecord: cr}
	cr.refs[ref] = struct{}{}
	return ref
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
	}
	sr.cm.records[id] = rec // TODO: save to db

	return rec.ref(), nil
}

func (sr *cacheRef) Size() (int64, error) {
	return -1, errors.New("Size not implemented")
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

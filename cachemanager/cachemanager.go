package cachemanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/mount"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/snapshot"
)

const dbFile = "cache.db"

var (
	errLocked   = errors.New("locked")
	errNotFound = errors.New("not found")
	errInvalid  = errors.New("invalid")
)

type CacheManagerOpt struct {
	Snapshotter snapshot.Snapshotter
	Root        string
	GCPolicy    GCPolicy
}

// GCPolicy defines policy for garbage collection
type GCPolicy struct {
	MaxSize         uint64
	MaxKeepDuration time.Duration
}

// // CachePolicy defines policy for keeping a resource in cache
// type CachePolicy struct {
// 	Priority int
// 	LastUsed time.Time
// }
//
// func defaultCachePolicy() CachePolicy {
// 	return CachePolicy{Priority: 10, LastUsed: time.Now()}
// }

type SnapshotRef interface {
	Mountable
	ID() string
	Release() error
	Size() (int64, error)
	// Prepare() / ChainID() / Meta()
}

type ActiveRef interface {
	Mountable
	ID() string
	ReleaseActive() (SnapshotRef, error)
	ReleaseAndCommit(ctx context.Context) (SnapshotRef, error)
	Size() (int64, error)
}

type Mountable interface {
	Mount() ([]mount.Mount, error)
}

type CacheAccessor interface {
	Get(id string) (SnapshotRef, error)
	New(s SnapshotRef) (ActiveRef, error)
	GetActive(id string) (ActiveRef, error) // Rebase?
}

type CacheController interface {
	DiskUsage(ctx context.Context) ([]CacheRecord, error)
	Prune(ctx context.Context) (map[string]int64, error)
	GC(ctx context.Context) error
}

type CacheManager interface {
	CacheAccessor
	CacheController
	Close() error
}

type CacheRecord struct {
	ID     string
	Active bool
	InUse  bool
	Size   int64
	// Meta string
	// LastUsed time.Time
}

type cacheManager struct {
	db      *bolt.DB // note: no particual reason for bolt
	records map[string]*cacheRecord
	mu      sync.Mutex
	CacheManagerOpt
}

func NewCacheManager(opt CacheManagerOpt) (CacheManager, error) {
	if err := os.MkdirAll(opt.Root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", opt.Root)
	}

	p := filepath.Join(opt.Root, dbFile)
	db, err := bolt.Open(p, 0600, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open database file %s", p)
	}

	cm := &cacheManager{
		CacheManagerOpt: opt,
		db:              db,
		records:         make(map[string]*cacheRecord),
	}

	if err := cm.init(); err != nil {
		return nil, err
	}

	// cm.scheduleGC(5 * time.Minute)

	return cm, nil
}

func (cm *cacheManager) init() error {
	// load all refs from db
	// compare with the walk from Snapshotter
	// delete items that are not in db (or implement broken transaction detection)
	// keep all refs in memory(maybe in future work on disk only or with lru)
	return nil
}

func (cm *cacheManager) Close() error {
	// TODO: allocate internal context and cancel it here
	return cm.db.Close()
}

func (cm *cacheManager) Get(id string) (SnapshotRef, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	rec, ok := cm.records[id]
	if !ok {
		// TODO: lazy-load from Snapshotter
		return nil, errors.Wrapf(errNotFound, "%s not found", id)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.active && !rec.sharedActive {
		if len(rec.refs) != 0 {
			return nil, errors.Wrapf(errLocked, "%s is locked", id)
		} else {
			rec.sharedActive = true
		}
	}

	return rec.ref(), nil
}
func (cm *cacheManager) New(s SnapshotRef) (ActiveRef, error) {
	id := generateID()

	var parent SnapshotRef
	var parentID string
	if s != nil {
		var err error
		parent, err = cm.Get(s.ID())
		if err != nil {
			return nil, err
		}
		parentID = parent.ID()
	}

	if _, err := cm.Snapshotter.Prepare(context.TODO(), id, parentID); err != nil {
		if parent != nil {
			parent.Release()
		}
		return nil, errors.Wrapf(err, "failed to prepare %s", id)
	}

	rec := &cacheRecord{
		active: true,
		id:     id,
		cm:     cm,
		refs:   make(map[*snapshotRef]struct{}),
		parent: parent,
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.records[id] = rec // TODO: save to db

	return rec.ref(), nil
}
func (cm *cacheManager) GetActive(id string) (ActiveRef, error) { // Rebase?
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, ok := cm.records[id]
	if !ok {
		return nil, errors.Wrapf(errNotFound, "%s not found", id)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !rec.active {
		return nil, errors.Wrapf(errInvalid, "%s is not active", id)
	}

	if rec.sharedActive || len(rec.refs) != 0 {
		return nil, errors.Wrapf(errLocked, "%s is locked", id)
	}

	return rec.ref(), nil
}

func (cm *cacheManager) DiskUsage(ctx context.Context) ([]CacheRecord, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var du []CacheRecord

	for id, cr := range cm.records {
		cr.mu.Lock()
		c := CacheRecord{
			ID:     id,
			Active: cr.active,
			InUse:  len(cr.refs) > 0,
			Size:   -1, // TODO
		}
		cr.mu.Unlock()
		du = append(du, c)
	}

	return du, nil
}

func (cm *cacheManager) Prune(ctx context.Context) (map[string]int64, error) {
	return nil, errors.New("Prune not implemented")
}

func (cm *cacheManager) GC(ctx context.Context) error {
	return errors.New("GC not implemented")
}

type cacheRecord struct {
	mu           sync.Mutex
	active       bool
	sharedActive bool
	// meta   SnapMeta
	refs   map[*snapshotRef]struct{}
	id     string
	cm     *cacheManager
	parent SnapshotRef
}

// hold manager lock before calling
func (cr *cacheRecord) ref() *snapshotRef {
	ref := &snapshotRef{cacheRecord: cr}
	cr.refs[ref] = struct{}{}
	return ref
}

type snapshotRef struct {
	*cacheRecord
}

func (sr *snapshotRef) Mount() ([]mount.Mount, error) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.active {
		m, err := sr.cm.Snapshotter.Mounts(context.TODO(), sr.id)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to mount %s", sr.id)
		}
		return m, nil
	}

	return nil, errors.New("snapshot mount not implemented")
}

func (sr *snapshotRef) Release() error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release()
}

func (sr *snapshotRef) release() error {
	if sr.parent != nil {
		if err := sr.parent.(*snapshotRef).release(); err != nil {
			return err
		}
	}
	delete(sr.refs, sr)
	sr.sharedActive = false

	if len(sr.refs) == 0 {
		//go sr.cm.GC()
	}

	return nil
}

func (sr *snapshotRef) ReleaseActive() (SnapshotRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.active || sr.sharedActive || len(sr.refs) != 1 {
		return nil, errors.Wrapf(errInvalid, "invalid active")
	}

	if _, ok := sr.refs[sr]; !ok {
		return nil, errors.Wrapf(errInvalid, "invalid active")
	}

	sr.sharedActive = true

	return sr, nil
}

func (sr *snapshotRef) ReleaseAndCommit(ctx context.Context) (SnapshotRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()

	if !sr.active || sr.sharedActive {
		sr.mu.Unlock()
		return nil, errors.Wrapf(errInvalid, "invalid active")
	}
	if len(sr.refs) != 1 {
		sr.mu.Unlock()
		return nil, errors.Wrapf(errInvalid, "multiple active references")
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
		refs: make(map[*snapshotRef]struct{}),
	}
	sr.cm.records[id] = rec // TODO: save to db

	return rec.ref(), nil
}

func (sr *snapshotRef) Size() (int64, error) {
	return -1, errors.New("Size not implemented")
}

func (sr *snapshotRef) ID() string {
	return sr.id
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

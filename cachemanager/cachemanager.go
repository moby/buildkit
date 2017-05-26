package cachemanager

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/boltdb/bolt"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/snapshot"
)

const dbFile = "cache.db"

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
	Releasable
	Size() (int64, error)
	// Prepare() / ChainID() / Meta()
}

type ActiveRef interface {
	Mountable
	Releasable
	ReleaseAndCommit(ctx context.Context) (SnapshotRef, error)
	Size() (int64, error)
}

type Mountable interface {
	Mount() (string, error) // containerd.Mount
	Unmount() error
}

type Releasable interface {
	Release() error
}

type CacheAccessor interface {
	Get(id string) (SnapshotRef, error)
	New(id string, s SnapshotRef) (ActiveRef, error)
	GetActive(id string) (ActiveRef, error) // Rebase?
}

type CacheController interface {
	DiskUsage(ctx context.Context) (map[string]int64, error)
	Prune(ctx context.Context) (map[string]int64, error)
	GC(ctx context.Context) error
}

type CacheManager interface {
	CacheAccessor
	CacheController
	Close() error
}

type cacheManager struct {
	db    *bolt.DB // note: no particual reason for bolt
	items map[string]*cacheRecord
	CacheManagerOpt
}

type snapshotRef struct {
}

type cacheRecord struct {
	active bool
	// meta   SnapMeta
	refs map[*snapshotRef]struct{}
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
		items:           make(map[string]*cacheRecord),
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
	return nil, errors.New("Get not implemented")
}
func (cm *cacheManager) New(id string, s SnapshotRef) (ActiveRef, error) {
	return nil, errors.New("New not implemented")
}
func (cm *cacheManager) GetActive(id string) (ActiveRef, error) { // Rebase?
	return nil, errors.New("GetActive not implemented")
}

func (cm *cacheManager) DiskUsage(ctx context.Context) (map[string]int64, error) {
	return nil, errors.New("DiskUsage not implemented")
}

func (cm *cacheManager) Prune(ctx context.Context) (map[string]int64, error) {
	return nil, errors.New("Prune not implemented")
}

func (cm *cacheManager) GC(ctx context.Context) error {
	return errors.New("GC not implemented")
}

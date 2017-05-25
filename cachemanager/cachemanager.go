package cachemanager

import (
	"context"
	"time"

	"github.com/boltdb/bolt"
	"github.com/tonistiigi/buildkit_poc/snapshot"
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
}

type cacheManager struct {
	db    *bolt.DB // note: no particual reason for bolt
	items map[string]*cacheRecord
}

type snapshotRef struct {
}

type cacheRecord struct {
	active bool
	// meta   SnapMeta
	refs map[*snapshotRef]struct{}
}

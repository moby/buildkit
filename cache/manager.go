package cache

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/boltdb/bolt"
	cdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/snapshot"
)

const dbFile = "cache.db"

var (
	errLocked   = errors.New("locked")
	errNotFound = errors.New("not found")
	errInvalid  = errors.New("invalid")
)

type ManagerOpt struct {
	Snapshotter snapshot.Snapshotter
	Root        string
	GCPolicy    GCPolicy
}

type Accessor interface {
	Get(id string) (ImmutableRef, error)
	New(s ImmutableRef) (MutableRef, error)
	GetMutable(id string) (MutableRef, error) // Rebase?
}

type Controller interface {
	DiskUsage(ctx context.Context) ([]*client.UsageInfo, error)
	Prune(ctx context.Context) (map[string]int64, error)
	GC(ctx context.Context) error
}

type Manager interface {
	Accessor
	Controller
	Close() error
}

type cacheManager struct {
	db      *bolt.DB // note: no particual reason for bolt
	records map[string]*cacheRecord
	mu      sync.Mutex
	ManagerOpt
}

func NewManager(opt ManagerOpt) (Manager, error) {
	if err := os.MkdirAll(opt.Root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", opt.Root)
	}

	p := filepath.Join(opt.Root, dbFile)
	db, err := bolt.Open(p, 0600, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open database file %s", p)
	}

	cm := &cacheManager{
		ManagerOpt: opt,
		db:         db,
		records:    make(map[string]*cacheRecord),
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

func (cm *cacheManager) Get(id string) (ImmutableRef, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.get(id)
}
func (cm *cacheManager) get(id string) (ImmutableRef, error) {
	rec, ok := cm.records[id]
	if !ok {
		info, err := cm.Snapshotter.Stat(context.TODO(), id)
		if err != nil {
			return nil, err
		}
		if info.Kind != cdsnapshot.KindCommitted {
			return nil, errors.Wrapf(errInvalid, "can't lazy load active %s", id)
		}

		var parent ImmutableRef
		if info.Parent != "" {
			parent, err = cm.get(info.Parent)
			if err != nil {
				return nil, err
			}
		}

		rec = &cacheRecord{
			id:     id,
			cm:     cm,
			refs:   make(map[Mountable]struct{}),
			parent: parent,
			size:   sizeUnknown,
		}
		cm.records[id] = rec // TODO: store to db
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.mutable && !rec.frozen {
		if len(rec.refs) != 0 {
			return nil, errors.Wrapf(errLocked, "%s is locked", id)
		} else {
			rec.frozen = true
		}
	}

	return rec.ref(), nil
}
func (cm *cacheManager) New(s ImmutableRef) (MutableRef, error) {
	id := generateID()

	var parent ImmutableRef
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
		mutable: true,
		id:      id,
		cm:      cm,
		refs:    make(map[Mountable]struct{}),
		parent:  parent,
		size:    sizeUnknown,
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.records[id] = rec // TODO: save to db

	return rec.mref(), nil
}
func (cm *cacheManager) GetMutable(id string) (MutableRef, error) { // Rebase?
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, ok := cm.records[id]
	if !ok {
		return nil, errors.Wrapf(errNotFound, "%s not found", id)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !rec.mutable {
		return nil, errors.Wrapf(errInvalid, "%s is not mutable", id)
	}

	if rec.frozen || len(rec.refs) != 0 {
		return nil, errors.Wrapf(errLocked, "%s is locked", id)
	}

	return rec.mref(), nil
}

func (cm *cacheManager) DiskUsage(ctx context.Context) ([]*client.UsageInfo, error) {
	cm.mu.Lock()

	var du []*client.UsageInfo

	for id, cr := range cm.records {
		cr.mu.Lock()
		c := &client.UsageInfo{
			ID:      id,
			Mutable: cr.mutable,
			InUse:   len(cr.refs) > 0,
			Size:    cr.size,
		}
		if cr.mutable && len(cr.refs) > 0 && !cr.frozen {
			c.Size = 0 // size can not be determined because it is changing
		}
		cr.mu.Unlock()
		du = append(du, c)
	}
	cm.mu.Unlock()

	eg, ctx := errgroup.WithContext(ctx)

	for _, d := range du {
		if d.Size == sizeUnknown {
			func(d *client.UsageInfo) {
				eg.Go(func() error {
					ref, err := cm.Get(d.ID)
					if err != nil {
						d.Size = 0
						return nil
					}
					s, err := ref.Size(ctx)
					if err != nil {
						return err
					}
					d.Size = s
					return ref.Release()
				})
			}(d)
		}
	}

	if err := eg.Wait(); err != nil {
		return du, err
	}

	return du, nil
}

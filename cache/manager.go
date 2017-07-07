package cache

import (
	"context"
	"sync"

	"github.com/Sirupsen/logrus"
	cdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/snapshot"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

var (
	errLocked   = errors.New("locked")
	errNotFound = errors.New("not found")
	errInvalid  = errors.New("invalid")
)

type ManagerOpt struct {
	Snapshotter   snapshot.Snapshotter
	GCPolicy      GCPolicy
	MetadataStore *metadata.Store
}

type Accessor interface {
	Get(ctx context.Context, id string) (ImmutableRef, error)
	New(ctx context.Context, s ImmutableRef) (MutableRef, error)
	GetMutable(ctx context.Context, id string) (MutableRef, error) // Rebase?
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
	records map[string]*cacheRecord
	mu      sync.Mutex
	ManagerOpt
	md *metadata.Store
}

func NewManager(opt ManagerOpt) (Manager, error) {
	cm := &cacheManager{
		ManagerOpt: opt,
		md:         opt.MetadataStore,
		records:    make(map[string]*cacheRecord),
	}

	if err := cm.init(context.TODO()); err != nil {
		return nil, err
	}

	// cm.scheduleGC(5 * time.Minute)

	return cm, nil
}

func (cm *cacheManager) init(ctx context.Context) error {
	items, err := cm.md.All()
	if err != nil {
		return err
	}

	for _, si := range items {
		if _, err := cm.load(ctx, si.ID()); err != nil {
			logrus.Debugf("could not load snapshot %s, %v", si.ID(), err)
			cm.md.Clear(si.ID())
		}
	}
	return nil
}

func (cm *cacheManager) Close() error {
	// TODO: allocate internal context and cancel it here
	return cm.md.Close()
}

func (cm *cacheManager) Get(ctx context.Context, id string) (ImmutableRef, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.get(ctx, id)
}
func (cm *cacheManager) get(ctx context.Context, id string) (ImmutableRef, error) {
	rec, err := cm.load(ctx, id)
	if err != nil {
		return nil, err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.mutable {
		return nil, errors.Wrapf(errInvalid, "invalid mutable ref %s", id)
	}

	if rec.mutable && !rec.frozen {
		if len(rec.refs) != 0 {
			return nil, errors.Wrapf(errLocked, "%s is locked", id)
		} else {
			rec.frozen = true
		}
	}

	return rec.ref(), nil
}

func (cm *cacheManager) load(ctx context.Context, id string) (*cacheRecord, error) {
	if rec, ok := cm.records[id]; ok {
		return rec, nil
	}

	info, err := cm.Snapshotter.Stat(ctx, id)
	if err != nil {
		return nil, errors.Wrap(errNotFound, err.Error())
	}

	var parent ImmutableRef
	if info.Parent != "" {
		parent, err = cm.get(ctx, info.Parent)
		if err != nil {
			return nil, err
		}
	}

	md, _ := cm.md.Get(id)

	rec := &cacheRecord{
		mutable: info.Kind != cdsnapshot.KindCommitted,
		cm:      cm,
		refs:    make(map[Mountable]struct{}),
		parent:  parent,
		md:      &md,
	}
	cm.records[id] = rec // TODO: store to db
	return rec, nil
}

func (cm *cacheManager) New(ctx context.Context, s ImmutableRef) (MutableRef, error) {
	id := identity.NewID()

	var parent ImmutableRef
	var parentID string
	if s != nil {
		var err error
		parent, err = cm.Get(ctx, s.ID())
		if err != nil {
			return nil, err
		}
		parentID = parent.ID()
	}

	if _, err := cm.Snapshotter.Prepare(ctx, id, parentID); err != nil {
		if parent != nil {
			parent.Release(ctx)
		}
		return nil, errors.Wrapf(err, "failed to prepare %s", id)
	}

	md, _ := cm.md.Get(id)

	rec := &cacheRecord{
		mutable: true,
		cm:      cm,
		refs:    make(map[Mountable]struct{}),
		parent:  parent,
		md:      &md,
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.records[id] = rec // TODO: save to db

	return rec.mref(), nil
}
func (cm *cacheManager) GetMutable(ctx context.Context, id string) (MutableRef, error) { // Rebase?
	cm.mu.Lock()
	defer cm.mu.Unlock()

	rec, err := cm.load(ctx, id)
	if err != nil {
		return nil, err
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

	type cacheUsageInfo struct {
		refs    int
		parent  string
		size    int64
		mutable bool
	}

	m := make(map[string]*cacheUsageInfo, len(cm.records))
	rescan := make(map[string]struct{}, len(cm.records))

	for id, cr := range cm.records {
		cr.mu.Lock()
		c := &cacheUsageInfo{
			refs:    len(cr.refs),
			mutable: cr.mutable,
			size:    getSize(cr.md),
		}
		if cr.parent != nil {
			c.parent = cr.parent.ID()
		}
		if cr.mutable && c.refs > 0 && !cr.frozen {
			c.size = 0 // size can not be determined because it is changing
		}
		m[id] = c
		cr.mu.Unlock()
		rescan[id] = struct{}{}
	}
	cm.mu.Unlock()

	for {
		if len(rescan) == 0 {
			break
		}
		for id := range rescan {
			v := m[id]
			if v.refs == 0 && v.parent != "" {
				m[v.parent].refs--
				rescan[v.parent] = struct{}{}
			}
			delete(rescan, id)
		}
	}

	var du []*client.UsageInfo
	for id, cr := range m {
		c := &client.UsageInfo{
			ID:      id,
			Mutable: cr.mutable,
			InUse:   cr.refs > 0,
			Size:    cr.size,
		}
		du = append(du, c)
	}

	eg, ctx := errgroup.WithContext(ctx)

	for _, d := range du {
		if d.Size == sizeUnknown {
			func(d *client.UsageInfo) {
				eg.Go(func() error {
					ref, err := cm.Get(ctx, d.ID)
					if err != nil {
						d.Size = 0
						return nil
					}
					s, err := ref.Size(ctx)
					if err != nil {
						return err
					}
					d.Size = s
					return ref.Release(context.TODO())
				})
			}(d)
		}
	}

	if err := eg.Wait(); err != nil {
		return du, err
	}

	return du, nil
}

func IsLocked(err error) bool {
	return errors.Cause(err) == errLocked
}

package cacheimport

import (
	"context"

	"github.com/moby/buildkit/identity"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

func NewCacheKeyStorage(cc *CacheChains, w worker.Worker) (solver.CacheKeyStorage, solver.CacheResultStorage, error) {
	storage := &cacheKeyStorage{
		byID:   map[string]*itemWithOutgoingLinks{},
		byItem: map[*item]string{},
	}

	for _, it := range cc.items {
		if _, err := addItemToStorage(storage, it); err != nil {
			return nil, nil, err
		}
	}

	results := &cacheResultStorage{
		w:    w,
		byID: storage.byID,
	}

	return storage, results, nil
}

func addItemToStorage(k *cacheKeyStorage, it *item) (*itemWithOutgoingLinks, error) {
	if id, ok := k.byItem[it]; ok {
		if id == "" {
			return nil, errors.Errorf("invalid loop")
		}
		return k.byID[id], nil
	}

	var id string
	if len(it.links) == 0 {
		id = it.dgst.String()
	} else {
		id = identity.NewID()
	}

	k.byItem[it] = ""

	for i, m := range it.links {
		for l := range m {
			src, err := addItemToStorage(k, l.src)
			if err != nil {
				return nil, err
			}
			cl := nlink{
				input:    i,
				dgst:     it.dgst,
				selector: l.selector,
			}
			src.links[cl] = append(src.links[cl], id)
		}
	}

	k.byItem[it] = id

	itl := &itemWithOutgoingLinks{
		item:  it,
		links: map[nlink][]string{},
	}

	k.byID[id] = itl
	return itl, nil
}

type cacheKeyStorage struct {
	byID   map[string]*itemWithOutgoingLinks
	byItem map[*item]string
}

type itemWithOutgoingLinks struct {
	*item
	links map[nlink][]string
}

func (cs *cacheKeyStorage) Exists(id string) bool {
	_, ok := cs.byID[id]
	logrus.Debugf("exists-check %s %v", id, ok)
	return ok
}

func (cs *cacheKeyStorage) Walk(func(id string) error) error {
	return nil
}

func (cs *cacheKeyStorage) WalkResults(id string, fn func(solver.CacheResult) error) error {
	it, ok := cs.byID[id]
	if !ok {
		return nil
	}
	if res := it.result; res != nil {
		return fn(solver.CacheResult{ID: id, CreatedAt: it.resultTime})
	}
	return nil
}

func (cs *cacheKeyStorage) Load(id string, resultID string) (solver.CacheResult, error) {
	_, ok := cs.byID[id]
	if !ok {
		return solver.CacheResult{}, nil
	}
	return solver.CacheResult{ID: id}, nil
}

func (cs *cacheKeyStorage) AddResult(id string, res solver.CacheResult) error {
	return nil
}

func (cs *cacheKeyStorage) Release(resultID string) error {
	return nil
}
func (cs *cacheKeyStorage) AddLink(id string, link solver.CacheInfoLink, target string) error {
	return nil
}
func (cs *cacheKeyStorage) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	it, ok := cs.byID[id]
	if !ok {
		return nil
	}
	for _, id := range it.links[nlink{
		dgst:     outputKey(link.Digest, int(link.Output)),
		input:    int(link.Input),
		selector: link.Selector.String(),
	}] {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

// TODO:
func (cs *cacheKeyStorage) WalkBacklinks(id string, fn func(id string, link solver.CacheInfoLink) error) error {
	return nil
}

func (cs *cacheKeyStorage) HasLink(id string, link solver.CacheInfoLink, target string) bool {
	l := nlink{
		dgst:     outputKey(link.Digest, int(link.Output)),
		input:    int(link.Input),
		selector: link.Selector.String(),
	}
	if it, ok := cs.byID[id]; ok {
		for _, id := range it.links[l] {
			if id == target {
				return true
			}
		}
	}
	return false
}

type cacheResultStorage struct {
	w    worker.Worker
	byID map[string]*itemWithOutgoingLinks
}

func (cs *cacheResultStorage) Save(res solver.Result) (solver.CacheResult, error) {
	return solver.CacheResult{}, errors.Errorf("importer is immutable")
}

func (cs *cacheResultStorage) Load(ctx context.Context, res solver.CacheResult) (solver.Result, error) {
	remote, err := cs.LoadRemote(ctx, res)
	if err != nil {
		return nil, err
	}

	ref, err := cs.w.FromRemote(ctx, remote)
	if err != nil {
		return nil, err
	}
	return worker.NewWorkerRefResult(ref, cs.w), nil
}

func (cs *cacheResultStorage) LoadRemote(ctx context.Context, res solver.CacheResult) (*solver.Remote, error) {
	it, ok := cs.byID[res.ID]
	if !ok {
		return nil, errors.WithStack(solver.ErrNotFound)
	}

	r := it.result
	if r == nil {
		return nil, errors.WithStack(solver.ErrNotFound)
	}

	return r, nil
}

func (cs *cacheResultStorage) Exists(id string) bool {
	it, ok := cs.byID[id]
	if !ok {
		return false
	}
	return it.result != nil
}

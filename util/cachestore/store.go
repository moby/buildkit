package cachestore

import (
	"context"
	"strings"

	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type Record struct {
	ID        int                          `json:"id"`
	Parents   map[int]map[*Record]struct{} `json:"-"`
	Children  []Link                       `json:"children,omitempty"`
	Digest    digest.Digest                `json:"digest,omitempty"`
	Random    bool                         `json:"random,omitempty"`
	ParentIDs map[int]map[int]struct{}     `json:"parents,omitempty"`
}

type Link struct {
	Input    int           `json:"input"`
	Output   int           `json:"output"`
	Digest   digest.Digest `json:"digest,omitempty"`
	Selector digest.Digest `json:"selector,omitempty"`
	Record   *Record       `json:"-"`
	ID       int           `json:"target,omitempty"`
}

type storeWithLinks interface {
	solver.CacheKeyStorage
	WalkLinksAll(id string, fn func(id string, link solver.CacheInfoLink) error) error
}

func Records(ctx context.Context, store solver.CacheKeyStorage) ([]*Record, error) {
	swl, ok := store.(storeWithLinks)
	if !ok {
		return nil, errors.New("cache store does not support walking all links")
	}

	roots := []string{}
	if err := store.Walk(func(id string) error {
		if strings.HasPrefix(string(id), "random:") || strings.HasPrefix(string(id), "sha256:") {
			roots = append(roots, id)
		}
		return nil
	}); err != nil {
		return nil, errors.Wrap(err, "failed to walk cache keys")
	}

	records := map[string]*Record{}
	for _, id := range roots {
		_, err := loadRecord(ctx, swl, id, records)
		if err != nil {
			return nil, err
		}
	}

	arr := []*Record{}
	for _, rec := range roots {
		r, ok := records[rec]
		if !ok {
			return nil, errors.Errorf("record %s not found in cache", rec)
		}
		arr = setIndex(r, arr)
	}
	for _, rec := range arr {
		setLinkIDs(rec)
	}

	return arr, nil
}

func setLinkIDs(rec *Record) {
	for i, child := range rec.Children {
		child.ID = child.Record.ID
		rec.Children[i] = child
	}
	if rec.Parents != nil {
		rec.ParentIDs = make(map[int]map[int]struct{})
		for input, m := range rec.Parents {
			rec.ParentIDs[input] = make(map[int]struct{})
			for parent := range m {
				rec.ParentIDs[input][parent.ID] = struct{}{}
			}
		}
	}
}

func setIndex(rec *Record, arr []*Record) []*Record {
	if rec.ID > 0 {
		return arr
	}
	arr = append(arr, rec)
	rec.ID = len(arr)
	for _, child := range rec.Children {
		arr = setIndex(child.Record, arr)
	}
	return arr
}

func loadRecord(ctx context.Context, store storeWithLinks, id string, out map[string]*Record) (*Record, error) {
	if r, ok := out[id]; ok {
		if r == nil {
			return nil, errors.Errorf("circular dependency detected for %s", id)
		}
		return r, nil
	}

	out[id] = nil

	rec := &Record{}
	if strings.HasPrefix(string(id), "random:") {
		rec.Digest = digest.Digest("sha256:" + strings.TrimPrefix(id, "random:"))
		rec.Random = true
	} else if strings.HasPrefix(string(id), "sha256:") {
		rec.Digest = digest.Digest(id)
	}

	err := store.WalkLinksAll(id, func(linkID string, link solver.CacheInfoLink) error {
		child, err := loadRecord(ctx, store, linkID, out)
		if err != nil {
			return errors.Wrapf(err, "failed to load link %s for %s", linkID, id)
		}
		rec.Children = append(rec.Children, Link{
			Input:    int(link.Input),
			Output:   int(link.Output),
			Selector: link.Selector,
			Record:   child,
			Digest:   link.Digest,
		})

		if child.Parents == nil {
			child.Parents = make(map[int]map[*Record]struct{})
		}
		m, ok := child.Parents[int(link.Input)]
		if !ok {
			m = make(map[*Record]struct{})
			child.Parents[int(link.Input)] = m
		}
		m[rec] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to walk links for %s", id)
	}
	out[id] = rec
	return rec, nil
}

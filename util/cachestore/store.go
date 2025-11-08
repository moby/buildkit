package cachestore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
)

type Record struct {
	ID       int                          `json:"id"`
	Parents  []Link                       `json:"parents,omitempty"`
	Children map[int]map[*Record]struct{} `json:"-"`
	Digest   digest.Digest                `json:"digest,omitempty"`
	Random   bool                         `json:"random,omitempty"`
	ChildIDs map[int]map[int]struct{}     `json:"children,omitempty"`
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
		return nil, fmt.Errorf("failed to walk cache keys"+": %w", err)
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
			return nil, fmt.Errorf("record %s not found in cache", rec)
		}
		arr = setIndex(r, arr)
	}
	for _, rec := range arr {
		setLinkIDs(rec)
	}

	return arr, nil
}

func setLinkIDs(rec *Record) {
	for i, parent := range rec.Parents {
		parent.ID = parent.Record.ID
		rec.Parents[i] = parent
	}
	if rec.Children != nil {
		rec.ChildIDs = make(map[int]map[int]struct{})
		for input, m := range rec.Children {
			rec.ChildIDs[input] = make(map[int]struct{})
			for child := range m {
				rec.ChildIDs[input][child.ID] = struct{}{}
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
	for _, links := range rec.Children {
		recs := slices.Collect(maps.Keys(links))
		slices.SortFunc(recs, func(i, j *Record) int {
			return cmp.Compare(i.Digest, j.Digest)
		})
		for _, child := range recs {
			arr = setIndex(child, arr)
		}
	}
	return arr
}

func loadRecord(ctx context.Context, store storeWithLinks, id string, out map[string]*Record) (*Record, error) {
	if r, ok := out[id]; ok {
		if r == nil {
			return nil, fmt.Errorf("circular dependency detected for %s", id)
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
			return fmt.Errorf("failed to load link %s for %s: %w", linkID, id, err)
		}
		child.Parents = append(child.Parents, Link{
			Input:    int(link.Input),
			Output:   int(link.Output),
			Selector: link.Selector,
			Record:   rec,
			Digest:   link.Digest,
		})

		if rec.Children == nil {
			rec.Children = make(map[int]map[*Record]struct{})
		}
		m, ok := rec.Children[int(link.Output)]
		if !ok {
			m = make(map[*Record]struct{})
			rec.Children[int(link.Output)] = m
		}
		m[child] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk links for %s: %w", id, err)
	}
	out[id] = rec
	return rec, nil
}

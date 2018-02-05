package solver

import (
	"fmt"
	"sync"

	digest "github.com/opencontainers/go-digest"
)

// EdgeIndex is a synchronous map for detecting edge collisions.
type EdgeIndex struct {
	mu sync.Mutex

	items    map[indexedDigest]map[indexedDigest]map[*edge]struct{}
	backRefs map[*edge]map[indexedDigest]map[indexedDigest]struct{}
}

func NewEdgeIndex() *EdgeIndex {
	return &EdgeIndex{
		items:    map[indexedDigest]map[indexedDigest]map[*edge]struct{}{},
		backRefs: map[*edge]map[indexedDigest]map[indexedDigest]struct{}{},
	}
}

func (ei *EdgeIndex) LoadOrStore(e *edge, dgst digest.Digest, index Index, deps [][]CacheKey) *edge {
	ei.mu.Lock()
	defer ei.mu.Unlock()

	if old := ei.load(e, dgst, index, deps); old != nil && !(!old.edge.Vertex.Options().IgnoreCache && e.edge.Vertex.Options().IgnoreCache) {
		return old
	}

	ei.store(e, dgst, index, deps)

	return nil
}

func (ei *EdgeIndex) Release(e *edge) {
	ei.mu.Lock()
	defer ei.mu.Unlock()

	for id, backRefs := range ei.backRefs[e] {
		for id2 := range backRefs {
			delete(ei.items[id][id2], e)
			if len(ei.items[id][id2]) == 0 {
				delete(ei.items[id], id2)
			}
		}
		if len(ei.items[id]) == 0 {
			delete(ei.items, id)
		}
	}
	delete(ei.backRefs, e)
}

func (ei *EdgeIndex) load(ignore *edge, dgst digest.Digest, index Index, deps [][]CacheKey) *edge {
	id := indexedDigest{dgst: dgst, index: index, depsCount: len(deps)}
	m, ok := ei.items[id]
	if !ok {
		return nil
	}
	if len(deps) == 0 {
		m2, ok := m[indexedDigest{}]
		if !ok {
			return nil
		}
		// prioritize edges with ignoreCache
		for e := range m2 {
			if e.edge.Vertex.Options().IgnoreCache && e != ignore {
				return e
			}
		}
		for e := range m2 {
			if e != ignore {
				return e
			}
		}
		return nil
	}

	matches := map[*edge]struct{}{}
	for i, keys := range deps {
		if i == 0 {
			for _, key := range keys {
				id := indexedDigest{dgst: getUniqueID(key), index: Index(i)}
				for e := range m[id] {
					if e != ignore {
						matches[e] = struct{}{}
					}
				}
			}
		} else {
		loop0:
			for match := range matches {
				for _, key := range keys {
					id := indexedDigest{dgst: getUniqueID(key), index: Index(i)}
					if m[id] != nil {
						if _, ok := m[id][match]; ok {
							continue loop0
						}
					}
				}
				delete(matches, match)
			}
		}
		if len(matches) == 0 {
			break
		}
	}

	// prioritize edges with ignoreCache
	for m := range matches {
		if m.edge.Vertex.Options().IgnoreCache {
			return m
		}
	}

	for m := range matches {
		return m
	}
	return nil
}

func (ei *EdgeIndex) store(e *edge, dgst digest.Digest, index Index, deps [][]CacheKey) {
	id := indexedDigest{dgst: dgst, index: index, depsCount: len(deps)}
	m, ok := ei.items[id]
	if !ok {
		m = map[indexedDigest]map[*edge]struct{}{}
		ei.items[id] = m
	}

	backRefsMain, ok := ei.backRefs[e]
	if !ok {
		backRefsMain = map[indexedDigest]map[indexedDigest]struct{}{}
		ei.backRefs[e] = backRefsMain
	}

	backRefs, ok := backRefsMain[id]
	if !ok {
		backRefs = map[indexedDigest]struct{}{}
		backRefsMain[id] = backRefs
	}

	if len(deps) == 0 {
		m2, ok := m[indexedDigest{}]
		if !ok {
			m2 = map[*edge]struct{}{}
			m[indexedDigest{}] = m2
		}
		m2[e] = struct{}{}

		backRefs[indexedDigest{}] = struct{}{}

		return
	}

	for i, keys := range deps {
		for _, key := range keys {
			id := indexedDigest{dgst: getUniqueID(key), index: Index(i)}
			m2, ok := m[id]
			if !ok {
				m2 = map[*edge]struct{}{}
				m[id] = m2
			}
			m2[e] = struct{}{}
			backRefs[id] = struct{}{}
		}
	}
}

type indexedDigest struct {
	dgst      digest.Digest
	index     Index
	depsCount int
}

type internalKeyT string

var internalKey = internalKeyT("buildkit/unique-cache-id")

func getUniqueID(k CacheKey) digest.Digest {
	internalV := k.GetValue(internalKey)
	if internalV != nil {
		return internalV.(digest.Digest)
	}

	dgstr := digest.SHA256.Digester()
	for _, inp := range k.Deps() {
		dgstr.Hash().Write([]byte(getUniqueID(inp)))
	}

	dgstr.Hash().Write([]byte(k.Digest()))
	dgstr.Hash().Write([]byte(fmt.Sprintf("%d", k.Output())))

	dgst := dgstr.Digest()
	k.SetValue(internalKey, dgst)

	return dgst
}

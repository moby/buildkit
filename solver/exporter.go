package solver

import (
	"context"
	"errors"
	"slices"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
)

type exporter struct {
	k             *CacheKey
	records       []*CacheRecord
	record        *CacheRecord
	recordCtxOpts func(context.Context) context.Context

	edge     *edge // for secondaryExporters
	override *bool
}

// remoteMatchesCompression reports whether every layer of remote is already in
// the requested compression, in which case resolving the result again cannot
// produce a better remote.
//
// It uses IsExactMediaType rather than IsMediaType because it needs to know
// what a blob is, not whether it is acceptable: an eStargz layer carries the
// plain gzip media type, so a chain of gzip descriptors is no evidence that the
// layers are eStargz, and asking for eStargz must still resolve. A partial
// match is not enough either; that case keeps the existing behaviour.
func remoteMatchesCompression(remote *Remote, comp compression.Config) bool {
	if remote == nil || len(remote.Descriptors) == 0 {
		return false
	}
	for _, desc := range remote.Descriptors {
		if !compression.IsExactMediaType(comp.Type, desc.MediaType) {
			return false
		}
	}
	return true
}

func addBacklinks(t CacheExporterTarget, cm *cacheManager, id string, bkm map[string][]CacheExporterRecord) ([]CacheExporterRecord, error) {
	out, ok := bkm[id]
	if ok && out != nil {
		return out, nil
	} else if ok && out == nil {
		return nil, nil
	}
	bkm[id] = nil

	m := map[digest.Digest][][]CacheLink{}
	isRoot := true
	if err := cm.backend.WalkBacklinks(id, func(id string, link CacheInfoLink) error {
		isRoot = false
		recs, err := addBacklinks(t, cm, id, bkm)
		if err != nil { // TODO: should we continue on error?
			return err
		}
		links := m[link.Digest]
		for int(link.Input) >= len(links) {
			links = append(links, nil)
		}
		for _, rec := range recs {
			links[int(link.Input)] = append(links[int(link.Input)], CacheLink{Src: rec, Selector: link.Selector.String()})
		}
		m[link.Digest] = links
		return nil
	}); err != nil {
		return nil, err
	}

	if isRoot {
		dgst, err := digest.Parse(id)
		if err == nil {
			rec, ok, err := t.Add(dgst, nil, nil)
			if err != nil {
				return nil, err
			}
			if ok && rec != nil {
				out = append(out, rec)
			}
		}
	}

	// validate that all inputs are present
	for dgst, links := range m {
		for _, links := range links {
			if len(links) == 0 {
				out = nil
				m[dgst] = nil
				break
			}
		}
	}

	for dgst, links := range m {
		if len(links) == 0 {
			continue
		}
		rec, ok, err := t.Add(dgst, links, nil)
		if err != nil {
			return nil, err
		}
		if !ok || rec == nil {
			continue
		}
		out = append(out, rec)
	}

	bkm[id] = out
	return out, nil
}

type contextT string

var (
	backlinkKey = contextT("solver/exporter/backlinks")
	resKey      = contextT("solver/exporter/res")
)

func (e *exporter) ExportTo(ctx context.Context, t CacheExporterTarget, opt CacheExportOpt) ([]CacheExporterRecord, error) {
	var bkm map[string][]CacheExporterRecord

	if bk := ctx.Value(backlinkKey); bk == nil {
		bkm = map[string][]CacheExporterRecord{}
		ctx = context.WithValue(ctx, backlinkKey, bkm)
	} else {
		bkm = bk.(map[string][]CacheExporterRecord)
	}

	var res map[*exporter][]CacheExporterRecord
	if r := ctx.Value(resKey); r == nil {
		res = map[*exporter][]CacheExporterRecord{}
		ctx = context.WithValue(ctx, resKey, res)
	} else {
		res = r.(map[*exporter][]CacheExporterRecord)
	}
	if v, ok := res[e]; ok {
		return v, nil
	}
	res[e] = nil

	deps := e.k.Deps()

	k := e.k.clone() // protect against *CacheKey internal ids mutation from other exports

	recKey := rootKey(k.Digest(), k.Output())
	results := []CacheExportResult{}

	addRecord := true

	if e.override != nil {
		addRecord = *e.override
	}

	exportRecord := opt.ExportRoots
	if len(deps) > 0 {
		exportRecord = true
	}

	records := slices.Clone(e.records)
	slices.SortStableFunc(records, compareCacheRecord)

	var remote *Remote
	var i int

	mainCtx := ctx
	if CacheOptGetterOf(ctx) == nil && e.recordCtxOpts != nil {
		ctx = e.recordCtxOpts(ctx)
	}
	v := e.record
	for exportRecord && addRecord {
		if v == nil {
			if i < len(records) {
				v = records[i]
				i++
			} else {
				break
			}
		}
		cm := v.cacheManager
		key := cm.getID(v.key)
		res, err := cm.backend.Load(key, v.ID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				v = nil
				continue
			}
			return nil, err
		}

		remotes, err := cm.results.LoadRemotes(ctx, res, opt.CompressionOpt, opt.Session)
		if err != nil {
			return nil, err
		}
		if len(remotes) > 0 {
			remote, remotes = remotes[0], remotes[1:] // pop the first element
		}
		if opt.CompressionOpt != nil {
			for _, r := range remotes { // record all remaining remotes as well
				results = append(results, CacheExportResult{
					CreatedAt:  v.CreatedAt,
					Result:     r,
					EdgeVertex: k.vtx,
					EdgeIndex:  k.output,
				})
			}
		}

		// Loading the result is expensive: for a record that came from an
		// imported cache it rebuilds the whole ref chain, one blob at a time,
		// under the cache manager's global lock, only for the ref to be
		// released a few lines later. It is needed to produce a remote when the
		// storage did not return one, or to get one in the requested
		// compression. Neither applies when the remote we already have is
		// entirely in that compression, so skip it then.
		needsResolve := remote == nil
		if !needsResolve && opt.CompressionOpt != nil {
			needsResolve = !remoteMatchesCompression(remote, *opt.CompressionOpt)
		}

		if needsResolve && opt.Mode != CacheExportModeRemoteOnly {
			res, err := cm.results.Load(ctx, res)
			if err != nil {
				if !errors.Is(err, cerrdefs.ErrNotFound) {
					return nil, err
				}
				remote = nil
			} else {
				remotes, err := opt.ResolveRemotes(ctx, res)
				if err != nil {
					return nil, err
				}
				res.Release(context.TODO())
				if remote == nil && len(remotes) > 0 {
					remote, remotes = remotes[0], remotes[1:] // pop the first element
				}
				if opt.CompressionOpt != nil {
					for _, r := range remotes { // record all remaining remotes as well
						results = append(results, CacheExportResult{
							CreatedAt:  v.CreatedAt,
							Result:     r,
							EdgeVertex: k.vtx,
							EdgeIndex:  k.output,
						})
					}
				}
			}
		}

		if remote != nil {
			results = append(results, CacheExportResult{
				CreatedAt:  v.CreatedAt,
				Result:     remote,
				EdgeVertex: k.vtx,
				EdgeIndex:  k.output,
			})
		}
		break
	}

	if remote != nil && opt.Mode == CacheExportModeMin {
		opt.Mode = CacheExportModeRemoteOnly
	}

	srcs := make([][]CacheLink, len(deps))

	for i, deps := range deps {
		for _, dep := range deps {
			rec, err := dep.CacheKey.Exporter.ExportTo(ctx, t, opt)
			if err != nil {
				continue
			}
			for _, r := range rec {
				srcs[i] = append(srcs[i], CacheLink{Src: r, Selector: string(dep.Selector)})
			}
		}
	}

	if e.edge != nil {
		for _, de := range e.edge.secondaryExporters {
			recs, err := de.cacheKey.CacheKey.Exporter.ExportTo(mainCtx, t, opt)
			if err != nil {
				continue
			}
			for _, r := range recs {
				srcs[de.index] = append(srcs[de.index], CacheLink{Src: r, Selector: de.cacheKey.Selector.String()})
			}
		}
	}

	if !opt.IgnoreBacklinks {
		for cm, id := range k.ids {
			_, err := addBacklinks(t, cm, id, bkm)
			if err != nil {
				return nil, err
			}
		}
	}

	// validate deps are present
	for _, deps := range srcs {
		if len(deps) == 0 {
			res[e] = nil
			return res[e], nil
		}
	}

	if v != nil && len(deps) == 0 {
		cm := v.cacheManager
		key := cm.getID(v.key)
		if err := cm.backend.WalkIDsByResult(v.ID, func(id string) error {
			if id == key {
				return nil
			}
			hasBacklinks := false
			cm.backend.WalkBacklinks(id, func(id string, link CacheInfoLink) error {
				hasBacklinks = true
				return nil
			})
			if hasBacklinks {
				return nil
			}

			dgst, err := digest.Parse(id)
			if err != nil {
				return nil
			}
			_, _, err = t.Add(dgst, nil, results)
			return err
		}); err != nil {
			return nil, err
		}
	}

	out, ok, err := t.Add(recKey, srcs, results)
	if err != nil {
		return nil, err
	}
	res[e] = []CacheExporterRecord{}
	if ok {
		res[e] = append(res[e], out)
	}
	return res[e], nil
}

func getBestResult(records []*CacheRecord) *CacheRecord {
	records = slices.Clone(records)
	slices.SortStableFunc(records, compareCacheRecord)
	if len(records) == 0 {
		return nil
	}
	return records[0]
}

func compareCacheRecord(a, b *CacheRecord) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if v := b.CreatedAt.Compare(a.CreatedAt); v != 0 {
		return v
	}
	return a.Priority - b.Priority
}

type mergedExporter struct {
	exporters []CacheExporter
}

func (e *mergedExporter) ExportTo(ctx context.Context, t CacheExporterTarget, opt CacheExportOpt) (er []CacheExporterRecord, err error) {
	for _, e := range e.exporters {
		r, err := e.ExportTo(ctx, t, opt)
		if err != nil {
			return nil, err
		}
		er = append(er, r...)
	}
	return
}

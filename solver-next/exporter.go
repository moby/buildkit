package solver

import (
	"context"

	digest "github.com/opencontainers/go-digest"
)

type exporter struct {
	k       *CacheKey
	records []*CacheRecord
	record  *CacheRecord

	res  []ExporterRecord
	edge *edge // for secondaryExporters
}

func addBacklinks(t ExporterTarget, rec ExporterRecord, cm *cacheManager, id string, bkm map[string]ExporterRecord) (ExporterRecord, error) {
	if rec == nil {
		var ok bool
		rec, ok = bkm[id]
		if ok {
			return rec, nil
		}
		_ = ok
	}
	if err := cm.backend.WalkBacklinks(id, func(id string, link CacheInfoLink) error {
		if rec == nil {
			rec = t.Add(link.Digest)
		}
		r, ok := bkm[id]
		if !ok {
			var err error
			r, err = addBacklinks(t, nil, cm, id, bkm)
			if err != nil {
				return err
			}
		}
		rec.LinkFrom(r, int(link.Input), link.Selector.String())
		return nil
	}); err != nil {
		return nil, err
	}
	if rec == nil {
		rec = t.Add(digest.Digest(id))
	}
	bkm[id] = rec
	return rec, nil
}

type backlinkT struct{}

var backlinkKey = backlinkT{}

func (e *exporter) ExportTo(ctx context.Context, t ExporterTarget, converter func(context.Context, Result) (*Remote, error)) ([]ExporterRecord, error) {
	var bkm map[string]ExporterRecord

	if bk := ctx.Value(backlinkKey); bk == nil {
		bkm = map[string]ExporterRecord{}
		ctx = context.WithValue(ctx, backlinkKey, bkm)
	} else {
		bkm = bk.(map[string]ExporterRecord)
	}

	if t.Visited(e) {
		return e.res, nil
	}

	deps := e.k.Deps()

	type expr struct {
		r        ExporterRecord
		selector digest.Digest
	}

	srcs := make([][]expr, len(deps))

	for i, deps := range deps {
		for _, dep := range deps {
			recs, err := dep.CacheKey.ExportTo(ctx, t, converter)
			if err != nil {
				return nil, nil
			}
			for _, r := range recs {
				srcs[i] = append(srcs[i], expr{r: r, selector: dep.Selector})
			}
		}
	}

	if e.edge != nil {
		for _, de := range e.edge.secondaryExporters {
			recs, err := de.cacheKey.CacheKey.ExportTo(ctx, t, converter)
			if err != nil {
				return nil, nil
			}
			for _, r := range recs {
				srcs[de.index] = append(srcs[de.index], expr{r: r, selector: de.cacheKey.Selector})
			}
		}
	}

	rec := t.Add(rootKey(e.k.Digest(), e.k.Output()))

	for i, srcs := range srcs {
		for _, src := range srcs {
			rec.LinkFrom(src.r, i, src.selector.String())
		}
	}

	for cm, id := range e.k.ids {
		if _, err := addBacklinks(t, rec, cm, id, bkm); err != nil {
			return nil, err
		}
	}

	if e.record == nil && len(e.k.Deps()) > 0 {
		e.record = getBestResult(e.records)
	}

	var remote *Remote

	if v := e.record; v != nil && len(e.k.Deps()) > 0 {
		cm := v.cacheManager
		res, err := cm.backend.Load(cm.getID(v.key), v.ID)
		if err != nil {
			return nil, err
		}

		remote, err = cm.results.LoadRemote(ctx, res)
		if err != nil {
			return nil, err
		}

		if remote == nil {
			res, err := cm.results.Load(ctx, res)
			if err != nil {
				return nil, err
			}
			remote, err = converter(ctx, res)
			if err != nil {
				return nil, err
			}
			res.Release(context.TODO())
		}

		if remote != nil {
			rec.AddResult(v.CreatedAt, remote)
		}
	}
	e.res = []ExporterRecord{rec}
	t.Visit(e)

	return e.res, nil
}

func getBestResult(records []*CacheRecord) *CacheRecord {
	var rec *CacheRecord
	for _, r := range records {
		if rec == nil || rec.CreatedAt.Before(r.CreatedAt) || (rec.CreatedAt.Equal(r.CreatedAt) && rec.Priority < r.Priority) {
			rec = r
		}
	}
	return rec
}

type mergedExporter struct {
	exporters []Exporter
}

func (e *mergedExporter) ExportTo(ctx context.Context, t ExporterTarget, converter func(context.Context, Result) (*Remote, error)) (er []ExporterRecord, err error) {
	for _, e := range e.exporters {
		r, err := e.ExportTo(ctx, t, converter)
		if err != nil {
			return nil, err
		}
		er = append(er, r...)
	}
	return
}

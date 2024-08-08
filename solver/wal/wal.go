package wal

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

const (
	flushInterval      = 200 * time.Millisecond
	preferredBatchSize = 1024
)

var ErrClosed = errors.Errorf("closed")

type WAL struct {
	s     solver.PersistentCacheKeyStorage
	inmem *solver.InMemoryCacheStorage

	closed        bool
	mu            sync.RWMutex
	linkEntries   []walLinkEntry
	resultEntries []walResultEntry
	batchSize     int64

	done     chan struct{}
	doneOnce sync.Once

	wg     errgroup.Group
	ticker *time.Ticker
}

func New(store solver.PersistentCacheKeyStorage) *WAL {
	w := &WAL{
		s:     store,
		inmem: solver.NewInMemoryCacheStorage(),
	}
	w.start()
	return w
}

func (w *WAL) Exists(id string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.inmem.Exists(id) || w.s.Exists(id)
}

func (w *WAL) Walk(fn func(id string) error) error {
	ids := func() (ids []string) {
		w.each(func(s solver.CacheKeyStorage) error {
			return s.Walk(func(id string) error {
				ids = append(ids, id)
				return nil
			})
		})
		return ids
	}

	for _, id := range ids() {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) WalkResults(id string, fn func(solver.CacheResult) error) error {
	results := func() (results []solver.CacheResult) {
		w.each(func(s solver.CacheKeyStorage) error {
			return s.WalkResults(id, func(res solver.CacheResult) error {
				results = append(results, res)
				return nil
			})
		})
		return results
	}

	for _, res := range results() {
		if err := fn(res); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) Load(id string, resultID string) (solver.CacheResult, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if res, err := w.inmem.Load(id, resultID); err == nil {
		return res, nil
	}
	return w.s.Load(id, resultID)
}

func (w *WAL) AddResult(id string, res solver.CacheResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrClosed
	}
	if err := w.inmem.AddResult(id, res); err != nil {
		return err
	}
	w.resultEntries = append(w.resultEntries, walResultEntry{
		ID:     id,
		Result: res,
	})
	w.inc()
	return nil
}

func (w *WAL) Release(resultID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.inmem.Release(resultID); err == nil {
		// Just in case there's a duplicate entry.
		// We don't want any inconsistency.
		_ = w.s.Release(resultID)
	}
	return w.s.Release(resultID)
}

func (w *WAL) WalkIDsByResult(resultID string, fn func(string) error) error {
	ids := func() (ids []string) {
		w.each(func(s solver.CacheKeyStorage) error {
			return s.WalkIDsByResult(resultID, func(id string) error {
				ids = append(ids, id)
				return nil
			})
		})
		return ids
	}

	for _, id := range ids() {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) AddLink(id string, link solver.CacheInfoLink, target string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrClosed
	}

	if err := w.inmem.AddLink(id, link, target); err != nil {
		return err
	}
	w.linkEntries = append(w.linkEntries, walLinkEntry{
		ID:     id,
		Link:   link,
		Target: target,
	})
	w.inc()
	return nil
}

func (w *WAL) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	ids := func() (ids []string) {
		w.each(func(s solver.CacheKeyStorage) error {
			return s.WalkLinks(id, link, func(id string) error {
				ids = append(ids, id)
				return nil
			})
		})
		return ids
	}

	for _, id := range ids() {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) HasLink(id string, link solver.CacheInfoLink, target string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.inmem.HasLink(id, link, target) || w.s.HasLink(id, link, target)
}

func (w *WAL) WalkBacklinks(id string, fn func(id string, link solver.CacheInfoLink) error) error {
	backlinks := func() (ids []string, links []solver.CacheInfoLink) {
		w.each(func(s solver.CacheKeyStorage) error {
			return s.WalkBacklinks(id, func(id string, link solver.CacheInfoLink) error {
				ids = append(ids, id)
				links = append(links, link)
				return nil
			})
		})
		return ids, links
	}

	ids, links := backlinks()
	for i, id := range ids {
		if err := fn(id, links[i]); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) each(fn func(s solver.CacheKeyStorage) error) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, s := range []solver.CacheKeyStorage{w.inmem, w.s} {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

func (w *WAL) Close() error {
	w.doneOnce.Do(func() {
		close(w.done)
	})
	if err := w.wg.Wait(); err != nil {
		bklog.G(context.TODO()).Warn("error closing wal", logrus.WithError(err))
	}

	if c, ok := w.s.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (w *WAL) start() {
	w.done = make(chan struct{})

	w.ticker = time.NewTicker(flushInterval)
	w.ticker.Stop()

	w.wg.Go(w.loop)
}

func (w *WAL) inc() error {
	w.batchSize++
	if w.batchSize >= preferredBatchSize {
		w.ticker.Stop()
		return w.flush()
	}

	// Preferred batch size has not been reached.
	// Reset the flush interval.
	w.ticker.Reset(flushInterval)
	return nil
}

func (w *WAL) loop() error {
	for {
		select {
		case <-w.ticker.C:
			if err := w.Flush(); err != nil {
				return err
			}
		case <-w.done:
			return w.close()
		}
	}
}

func (w *WAL) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.flush()
}

func (w *WAL) flush() error {
	if err := w.s.Update(func(tx solver.CacheKeyStorageUpdate) error {
		for _, link := range w.linkEntries {
			if err := tx.AddLink(link.ID, link.Link, link.Target); err != nil {
				return err
			}
		}
		for _, res := range w.resultEntries {
			if err := tx.AddResult(res.ID, res.Result); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	w.linkEntries = w.linkEntries[:0]
	w.resultEntries = w.resultEntries[:0]
	w.inmem.Reset()
	return nil
}

func (w *WAL) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ticker.Stop()
	err := w.flush()
	w.closed = true
	return err
}

type walLinkEntry struct {
	ID     string
	Link   solver.CacheInfoLink
	Target string
}

type walResultEntry struct {
	ID     string
	Result solver.CacheResult
}

package boltutil

import (
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

// DB gates transactions so compaction can replace the underlying handle.
type DB struct {
	path     string
	mode     fs.FileMode
	opts     *bolt.Options
	pageSize int

	gate gate

	// hmu serializes operations that close or replace the handle.
	hmu         sync.Mutex
	bdb         *bolt.DB
	closed      bool
	lastCompact time.Time
}

var (
	_ db.DB        = (*DB)(nil)
	_ db.Compactor = (*DB)(nil)
)

func Open(p string, mode fs.FileMode, options *bolt.Options) (*DB, error) {
	if options == nil {
		options = bolt.DefaultOptions
	}
	opts := *options
	bdb, err := bolt.Open(p, mode, &opts)
	if err != nil {
		return nil, err
	}
	if !opts.ReadOnly {
		// Cleanup failure must not make SafeOpen reset a healthy database.
		if err := os.Remove(compactPath(p)); err != nil && !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- Database paths are daemon configuration, not build input.
			bklog.L.WithError(err).Warnf("failed to remove stale compaction file for %s", p)
		}
	}
	return &DB{path: p, mode: mode, opts: &opts, pageSize: bdb.Info().PageSize, bdb: bdb}, nil
}

func (d *DB) View(fn func(*bolt.Tx) error) error {
	d.gate.enter()
	defer d.gate.exit()
	return d.bdb.View(fn)
}

func (d *DB) Update(fn func(*bolt.Tx) error) error {
	d.gate.enter()
	defer d.gate.exit()
	return d.bdb.Update(fn)
}

func (d *DB) Close() error {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.bdb.Close()
}

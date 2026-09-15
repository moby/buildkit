package boltutil

import (
	"context"
	stderrors "errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
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
	policy      *compaction.Scheduler
}

var (
	_ db.DB        = (*DB)(nil)
	_ db.Compactor = (*DB)(nil)
)

func Open(p string, mode fs.FileMode, options *bolt.Options) (*DB, error) {
	return OpenWithCompaction(p, mode, options, compaction.DefaultConfig())
}

// OpenWithCompaction opens a database with an idle maintenance policy.
// Read-only databases never start maintenance or write policy state.
func OpenWithCompaction(p string, mode fs.FileMode, options *bolt.Options, config compaction.Config) (*DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if options == nil {
		options = bolt.DefaultOptions
	}
	opts := *options
	newDB := !fileHasContent(p)
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
	d := &DB{path: p, mode: mode, opts: &opts, pageSize: bdb.Info().PageSize, bdb: bdb}
	if !opts.ReadOnly {
		backend := policyBackend{d}
		// Policy state is advisory. Failure must not trigger SafeOpen's database recovery.
		var state compaction.State
		if !newDB {
			state = backend.load()
		}
		ctx := bklog.WithLogger(context.Background(), bklog.L.WithField("database", p))
		d.policy, _ = compaction.New(ctx, config, state, backend)
	}
	return d, nil
}

func (d *DB) View(fn func(*bolt.Tx) error) error {
	if d.policy != nil {
		d.policy.Begin(false)
		defer d.policy.End(false)
	}
	if !d.gate.enter() {
		return bolterrors.ErrDatabaseNotOpen
	}
	defer d.gate.exit()
	return d.bdb.View(fn)
}

func (d *DB) Update(fn func(*bolt.Tx) error) error {
	committed := false
	if d.policy != nil {
		d.policy.Begin(true)
		defer func() { d.policy.End(committed) }()
	}
	if !d.gate.enter() {
		return bolterrors.ErrDatabaseNotOpen
	}
	defer d.gate.exit()
	err := d.bdb.Update(fn)
	committed = err == nil
	return err
}

func (d *DB) Close() error {
	if d.policy != nil {
		d.policy.Stop()
	}
	d.hmu.Lock()
	defer d.hmu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.gate.close()
	var err error
	if d.policy != nil {
		err = d.policy.Close()
	}
	return stderrors.Join(err, d.bdb.Close())
}

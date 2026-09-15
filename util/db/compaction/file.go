package compaction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
)

// NewFile starts a policy with checkpoints beside the database. Its lifetime is
// controlled by Stop and Close, rather than by a build request's context.
func NewFile(config Config, path string, fresh bool, database db.Compactor) (*Scheduler, error) {
	backend := fileBackend{path: path, database: database}
	var state State
	if !fresh {
		state = backend.load()
	}
	ctx := bklog.WithLogger(context.Background(), bklog.L.WithField("database", path))
	return New(ctx, config, state, backend)
}

type fileBackend struct {
	path     string
	database db.Compactor
}

func (b fileBackend) CompactionStats() (db.CompactionStats, error) {
	return b.database.CompactionStats()
}

func (b fileBackend) Compact(ctx context.Context, opt db.CompactOptions) (db.CompactResult, error) {
	return b.database.Compact(ctx, opt)
}

func (b fileBackend) Save(state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := b.path + ".compact-state"
	f, err := os.CreateTemp(filepath.Dir(path), ".compact-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (b fileBackend) load() State {
	var state State
	data, err := os.ReadFile(b.path + ".compact-state")
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	if err != nil {
		bklog.L.WithError(err).Warnf("failed to load compaction policy for %s", b.path)
		return State{}
	}
	return state
}

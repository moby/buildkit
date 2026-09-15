package boltutil

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/pkg/errors"
)

type policyBackend struct{ db *DB }

func (b policyBackend) CompactionStats() (db.CompactionStats, error) {
	return b.db.CompactionStats()
}

func (b policyBackend) Compact(ctx context.Context, opt db.CompactOptions) (db.CompactResult, error) {
	return b.db.Compact(ctx, opt)
}

func (b policyBackend) Save(state compaction.State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := b.db.path + ".compact-state"
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

func (b policyBackend) load() compaction.State {
	var state compaction.State
	data, err := os.ReadFile(b.db.path + ".compact-state")
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	if err != nil {
		bklog.L.WithError(err).Warnf("failed to load compaction policy for %s", b.db.path)
		return compaction.State{}
	}
	return state
}

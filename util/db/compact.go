package db

import (
	"context"
	"time"
)

// CompactOptions controls when a database is compacted. Zero thresholds disable checks.
type CompactOptions struct {
	MinReclaimBytes   int64
	MinReclaimPercent int64
	// MinInterval also throttles failed attempts.
	MinInterval time.Duration
	// PauseTimeout bounds draining transactions. Zero selects the default.
	PauseTimeout time.Duration
	// CopyTimeout bounds copying independently of the drain. Zero uses the
	// caller's context. Filesystem calls cannot be interrupted.
	CopyTimeout time.Duration
}

type CompactResult struct {
	// Compacted remains true if a failure occurs after replacement.
	Compacted  bool
	Reason     string
	SizeBefore int64
	SizeAfter  int64
	// Reclaimable includes both free and pending pages.
	Reclaimable int64
	Duration    time.Duration
}

type Compactor interface {
	Compact(ctx context.Context, opt CompactOptions) (CompactResult, error)
}

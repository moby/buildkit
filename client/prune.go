package client

import (
	"context"
	"io"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
)

func (c *Client) Prune(ctx context.Context, ch chan UsageInfo, opts ...PruneOption) error {
	info := &PruneInfo{}
	for _, o := range opts {
		o.SetPruneOption(info)
	}

	req := &controlapi.PruneRequest{
		Filter:       info.Filter,
		KeepDuration: int64(info.KeepDuration),
		MinStorage:   int64(info.MinStorage),
		MaxStorage:   int64(info.MaxStorage),
		Free:         int64(info.Free),
	}
	if info.All {
		req.All = true
	}
	cl, err := c.ControlClient().Prune(ctx, req)
	if err != nil {
		return errors.Wrap(err, "failed to call prune")
	}

	for {
		d, err := cl.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if ch != nil {
			ch <- UsageInfo{
				ID:          d.ID,
				Mutable:     d.Mutable,
				InUse:       d.InUse,
				Size:        d.Size,
				Parents:     d.Parents,
				CreatedAt:   d.CreatedAt.AsTime(),
				Description: d.Description,
				UsageCount:  int(d.UsageCount),
				LastUsedAt: func() *time.Time {
					if d.LastUsedAt != nil {
						ts := d.LastUsedAt.AsTime()
						return &ts
					}
					return nil
				}(),
				RecordType: UsageRecordType(d.RecordType),
				Shared:     d.Shared,
			}
		}
	}
}

type PruneOption interface {
	SetPruneOption(*PruneInfo)
}

type PruneInfo struct {
	All          bool          `json:"all"`
	Filter       []string      `json:"filter"`
	KeepDuration time.Duration `json:"keepDuration"`

	MinStorage int64 `json:"minStorage"`
	MaxStorage int64 `json:"maxStorage"`
	Free       int64 `json:"free"`
}

type pruneOptionFunc func(*PruneInfo)

func (f pruneOptionFunc) SetPruneOption(pi *PruneInfo) {
	f(pi)
}

var PruneAll = pruneOptionFunc(func(pi *PruneInfo) {
	pi.All = true
})

func WithKeepOpt(duration time.Duration, minStorage int64, maxStorage int64, free int64) PruneOption {
	return pruneOptionFunc(func(pi *PruneInfo) {
		pi.KeepDuration = duration
		pi.MinStorage = minStorage
		pi.MaxStorage = maxStorage
		pi.Free = free
	})
}

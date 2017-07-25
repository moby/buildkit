package client

import (
	"context"
	"sort"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
)

type UsageInfo struct {
	ID      string
	Mutable bool
	InUse   bool
	Size    int64

	CreatedAt   time.Time
	LastUsedAt  *time.Time
	UsageCount  int
	Parent      string
	Description string
}

func (c *Client) DiskUsage(ctx context.Context) ([]*UsageInfo, error) {
	resp, err := c.controlClient().DiskUsage(ctx, &controlapi.DiskUsageRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get diskusage")
	}

	var du []*UsageInfo

	for _, d := range resp.Record {
		du = append(du, &UsageInfo{
			ID:          d.ID,
			Mutable:     d.Mutable,
			InUse:       d.InUse,
			Size:        d.Size_,
			Parent:      d.Parent,
			CreatedAt:   d.CreatedAt,
			Description: d.Description,
			UsageCount:  int(d.UsageCount),
			LastUsedAt:  d.LastUsedAt,
		})
	}

	sort.Slice(du, func(i, j int) bool {
		return du[i].Size > du[j].Size
	})

	return du, nil
}

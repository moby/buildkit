package client

import (
	"context"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
)

type UsageInfo struct {
	ID      string
	Mutable bool
	InUse   bool
	Size    int64
	// Meta string
	// LastUsed time.Time
}

func (c *Client) DiskUsage(ctx context.Context) ([]*UsageInfo, error) {
	resp, err := c.controlClient().DiskUsage(ctx, &controlapi.DiskUsageRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get diskusage")
	}

	var du []*UsageInfo

	for _, d := range resp.Record {
		du = append(du, &UsageInfo{
			ID:      d.ID,
			Mutable: d.Mutable,
			InUse:   d.InUse,
			Size:    d.Size_,
		})
	}

	return du, nil
}

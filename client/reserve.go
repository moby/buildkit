package client

import (
	"context"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
)

func (c *Client) Reserve(ctx context.Context) error {
	_, err := c.controlClient().Reserve(ctx, &controlapi.ReserveRequest{})
	if err != nil {
		return errors.Wrap(err, "failed to call reserve")
	}
	return nil
}

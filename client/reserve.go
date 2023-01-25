package client

import (
	"context"

	"github.com/pkg/errors"

	controlapi "github.com/moby/buildkit/api/services/control"
)

func (c *Client) Reserve(ctx context.Context) error {
	_, err := c.ControlClient().Reserve(ctx, &controlapi.ReserveRequest{})
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

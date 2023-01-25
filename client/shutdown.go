package client

import (
	"context"

	"github.com/pkg/errors"

	controlapi "github.com/moby/buildkit/api/services/control"
)

func (c *Client) ShutdownIfIdle(ctx context.Context) (bool, int, error) {
	res, err := c.ControlClient().ShutdownIfIdle(
		ctx, &controlapi.ShutdownIfIdleRequest{})
	if err != nil {
		return false, 0, errors.Wrap(err, "failed to call shutdown if idle")
	}
	return res.GetWillShutdown(), int(res.GetNumSessions()), nil
}

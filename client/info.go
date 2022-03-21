package client

import (
	"context"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/pkg/errors"
)

type InfoResponse struct {
	BuildkitVersion Version
}

func (c *Client) Info(ctx context.Context) (*InfoResponse, error) {
	res, err := c.controlClient().Info(ctx, &controlapi.InfoRequest{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to call info")
	}
	return &InfoResponse{
		BuildkitVersion: Version{
			Package:  res.BuildkitVersion.Package,
			Version:  res.BuildkitVersion.Version,
			Revision: res.BuildkitVersion.Revision,
		},
	}, nil
}

type Version struct {
	Package  string
	Version  string
	Revision string
}

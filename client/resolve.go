package client

import (
	"context"

	controlapi "github.com/moby/buildkit/api/services/control"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

func (c *Client) ResolveImageConfig(ctx context.Context, ref string, platform *specs.Platform) (digest.Digest, []byte, error) {
	var p *opspb.Platform
	if platform != nil {
		p = &opspb.Platform{
			OS:           platform.OS,
			Architecture: platform.Architecture,
			Variant:      platform.Variant,
			OSVersion:    platform.OSVersion,
			OSFeatures:   platform.OSFeatures,
		}
	}
	rsp, err := c.controlClient().ResolveImageConfig(ctx, &controlapi.ResolveImageConfigRequest{
		Ref:      ref,
		Platform: p,
	})
	if err != nil {
		return "", nil, err
	}

	return rsp.Digest, rsp.Config, err
}

package client

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

var validationTests = []func(t *testing.T, sb integration.Sandbox){
	testValidateNullConfig,
	testValidateInvalidConfig,
}

func testValidateNullConfig(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		def, err := llb.Scratch().Marshal(ctx)
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, client.SolveRequest{
			Evaluate:   true,
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		res.AddMeta("containerimage.config", []byte("null"))
		return res, nil
	}

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
			},
		},
	}, "", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid null image config for export")
}

func testValidateInvalidConfig(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		def, err := llb.Scratch().Marshal(ctx)
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, client.SolveRequest{
			Evaluate:   true,
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		var img ocispecs.Image
		img.Platform = ocispecs.Platform{
			Architecture: "amd64",
		}
		dt, err := json.Marshal(img)
		if err != nil {
			return nil, err
		}
		res.AddMeta("containerimage.config", dt)
		return res, nil
	}

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
			},
		},
	}, "", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid image config for export: missing os")
}

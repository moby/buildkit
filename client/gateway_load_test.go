package client

import (
	"context"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func testClientGatewayLoadNoSolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("busybox:latest").
		Run(llb.Shlexf("sh -c 'echo %s; false'", identity.NewID())).
		Root().Marshal(ctx)
	require.NoError(t, err)

	var evalErr error
	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", func(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
		caps := c.BuildOpts().Caps
		if err := caps.Supports(gwpb.CapGatewayEvaluateLoad); err != nil {
			return nil, err
		}
		res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
		if err != nil {
			return nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		if err := ref.Load(ctx); err != nil {
			return nil, errors.Wrap(err, "load")
		}
		// loading again is valid
		if err := ref.Load(ctx); err != nil {
			return nil, errors.Wrap(err, "second load")
		}
		evalErr = ref.Evaluate(ctx)
		return gwclient.NewResult(), nil
	}, nil)
	require.NoError(t, err)
	require.ErrorContains(t, evalErr, "did not complete successfully")
	require.ErrorContains(t, evalErr, "exit code: 1")
}

// build 2 has no secrets and can only use build 1's session if build 1 loaded the ref
func testClientGatewayLoadSessionSecret(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	err = runSharedSecretBuilds(t, sb, c, false)
	require.ErrorContains(t, err, "secret s: not found")

	err = runSharedSecretBuilds(t, sb, c, true)
	require.NoError(t, err)
}

func runSharedSecretBuilds(t *testing.T, sb integration.Sandbox, c *Client, load bool) error {
	ctx, cancel := context.WithTimeoutCause(sb.Context(), 2*time.Minute, errors.New("shared secret builds timed out"))
	defer cancel()

	def, err := llb.Image("busybox:latest").
		Run(
			llb.Shlexf(`sh -c '[ "$(cat /run/secrets/s)" = "secret-value" ] && echo %s'`, identity.NewID()),
			llb.AddSecret("/run/secrets/s", llb.SecretID("s")),
		).
		Root().Marshal(ctx)
	require.NoError(t, err)

	loaded := make(chan struct{})
	release := make(chan struct{})

	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		_, err := c.Build(egCtx, SolveOpt{
			Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
				"s": []byte("secret-value"),
			})},
		}, "buildkit_test", func(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
			caps := c.BuildOpts().Caps
			if err := caps.Supports(gwpb.CapGatewayEvaluateLoad); err != nil {
				return nil, err
			}
			res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
			if err != nil {
				return nil, err
			}
			if load {
				ref, err := res.SingleRef()
				if err != nil {
					return nil, err
				}
				if err := ref.Load(ctx); err != nil {
					return nil, errors.Wrap(err, "load")
				}
			}
			close(loaded)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			return gwclient.NewResult(), nil
		}, nil)
		return errors.Wrap(err, "build 1")
	})

	var build2Err error
	eg.Go(func() error {
		defer close(release)
		select {
		case <-loaded:
		case <-egCtx.Done():
			return context.Cause(egCtx)
		}
		_, build2Err = c.Build(egCtx, SolveOpt{}, "buildkit_test", func(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
			return c.Solve(ctx, gwclient.SolveRequest{
				Definition: def.ToPB(),
				Evaluate:   true,
			})
		}, nil)
		return nil
	})

	require.NoError(t, eg.Wait())
	return build2Err
}

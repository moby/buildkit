package client

import (
	"context"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func testClientGatewaySharedExecError(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx, cancel := context.WithTimeoutCause(sb.Context(), time.Minute, errors.New("shared exec error test timed out"))
	defer cancel()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("busybox:latest").Run(
		llb.Shlexf("sh -c 'echo %s > /data && exit 1'", identity.NewID()),
	).Root().Marshal(ctx)
	require.NoError(t, err)

	solve := func(ctx context.Context, c gwclient.Client) (*errdefs.SolveError, error) {
		_, err := c.Solve(ctx, gwclient.SolveRequest{
			Definition: def.ToPB(),
			Evaluate:   true,
		})
		var se *errdefs.SolveError
		if !errors.As(err, &se) {
			return nil, errors.Errorf("expected SolveError, got %v", err)
		}
		if len(se.MountIDs) != 1 || se.MountIDs[0] == "" {
			return nil, errors.Errorf("expected failed rootfs mount, got %v", se.MountIDs)
		}
		return se, nil
	}

	firstReady := make(chan struct{})
	secondReady := make(chan struct{})
	firstDone := make(chan struct{})
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		defer close(firstDone)
		_, err := c.Build(ctx, SolveOpt{}, "buildkit_test", func(ctx context.Context, c gwclient.Client) (*gwclient.Result, error) {
			if _, err := solve(ctx, c); err != nil {
				return nil, err
			}
			close(firstReady)
			// Keep the failed vertex alive until both builds hold its error.
			select {
			case <-secondReady:
				return gwclient.NewResult(), nil
			case <-ctx.Done():
				return nil, errors.WithStack(context.Cause(ctx))
			}
		}, nil)
		return err
	})
	eg.Go(func() error {
		_, err := c.Build(ctx, SolveOpt{}, "buildkit_test", func(ctx context.Context, gc gwclient.Client) (*gwclient.Result, error) {
			select {
			case <-firstReady:
			case <-ctx.Done():
				return nil, errors.WithStack(context.Cause(ctx))
			}
			se, err := solve(ctx, gc)
			if err != nil {
				return nil, err
			}
			close(secondReady)
			select {
			case <-firstDone:
			case <-ctx.Done():
				return nil, errors.WithStack(context.Cause(ctx))
			}

			// Discarding the first build must not let GC remove the snapshot
			// still needed by the second build's cached error.
			if err := c.Prune(ctx, nil, PruneAll); err != nil {
				return nil, err
			}
			ctr, err := gc.NewContainer(ctx, gwclient.NewContainerRequest{
				Mounts: []gwclient.Mount{{Dest: "/", ResultID: se.MountIDs[0]}},
			})
			if err != nil {
				return nil, err
			}
			if err := ctr.Release(ctx); err != nil {
				return nil, err
			}
			return gwclient.NewResult(), nil
		}, nil)
		return err
	})
	require.NoError(t, eg.Wait())
	checkAllReleasable(t, c, sb, true)
}

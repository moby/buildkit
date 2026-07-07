package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func testClientGatewayEmptyImageExec(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testemptyimage:latest"

	// push an empty image
	_, err = c.Build(sb.Context(), SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, "", func(ctx context.Context, c client.Client) (*client.Result, error) {
		return client.NewResult(), nil
	}, nil)
	require.NoError(t, err)

	_, err = c.Build(sb.Context(), SolveOpt{}, "", func(ctx context.Context, gw client.Client) (*client.Result, error) {
		// create an exec on that empty image (expected to fail, but not to panic)
		st := llb.Image(target).Run(
			llb.Args([]string{"echo", "hello"}),
		).Root()
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		_, err = gw.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
			Evaluate:   true,
		})
		require.ErrorContains(t, err, `process "echo hello" did not complete successfully`)
		return nil, nil
	}, nil)
	require.NoError(t, err)
}

func testClientGatewayEmptySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		r, err := c.Solve(ctx, client.SolveRequest{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}
		if r.Ref != nil || r.Refs != nil || r.Metadata != nil {
			return nil, errors.Errorf("got unexpected non-empty result %+v", r)
		}
		return r, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "", b, nil)
	require.NoError(t, err)
}

func testClientGatewayFailedSolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		return nil, errors.New("expected to fail")
	}

	_, err = c.Build(ctx, SolveOpt{}, "", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected to fail")
}

func testClientGatewayNilResult(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMergeDiff)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Image("busybox:latest")
		diff := llb.Diff(st, st)
		def, err := diff.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		res, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
			Evaluate:   true,
		})
		require.NoError(t, err)

		ref, err := res.SingleRef()
		require.NoError(t, err)

		dirEnts, err := ref.ReadDir(ctx, client.ReadDirRequest{
			Path: "/",
		})
		require.NoError(t, err)
		require.Len(t, dirEnts, 0)
		return nil, nil
	}

	_, err = c.Build(sb.Context(), SolveOpt{}, "", b, nil)
	require.NoError(t, err)
}

func testClientGatewaySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"
	optKey := "test-string"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		if c.BuildOpts().Product != product {
			return nil, errors.Errorf("expected product %q, got %q", product, c.BuildOpts().Product)
		}
		opts := c.BuildOpts().Opts
		testStr, ok := opts[optKey]
		if !ok {
			return nil, errors.Errorf(`build option %q missing`, optKey)
		}

		run := llb.Image("busybox:latest").Run(
			llb.ReadonlyRootFS(),
			llb.Args([]string{"/bin/sh", "-ec", `echo -n '` + testStr + `' > /out/foo`}),
		)
		st := run.AddMount("/out", llb.Scratch())

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal state")
		}

		r, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}

		read, err := r.Ref.ReadFile(ctx, client.ReadRequest{
			Filename: "/foo",
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to read result")
		}
		if testStr != string(read) {
			return nil, errors.Errorf("read back %q, expected %q", string(read), testStr)
		}
		return r, nil
	}

	tmpdir := t.TempDir()

	testStr := "This is a test"

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: tmpdir,
			},
		},
		FrontendAttrs: map[string]string{
			optKey: testStr,
		},
	}, product, b, nil)
	require.NoError(t, err)

	read, err := os.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, testStr, string(read))

	checkAllReleasable(t, c, sb, true)
}

func testClientSlowCacheRootfsRef(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		id := identity.NewID()
		input := llb.Scratch().File(
			llb.Mkdir("/found", 0o700).
				Mkfile("/found/data", 0o600, []byte(id)),
		)

		st := llb.Image("busybox:latest").Run(
			llb.Shlexf("echo hello"),
			// Only readonly mounts trigger slow cache errors.
			llb.AddMount("/src", input, llb.SourcePath("/notfound"), llb.Readonly),
		).Root()

		def1, err := st.Marshal(ctx)
		require.NoError(t, err)

		res1, err := c.Solve(ctx, client.SolveRequest{
			Definition: def1.ToPB(),
		})
		require.NoError(t, err)

		ref1, err := res1.SingleRef()
		require.NoError(t, err)

		// First stat should error because unlazy-ing the reference causes an error
		// in CalcSlowCache.
		_, err = ref1.StatFile(ctx, client.StatRequest{
			Path: ".",
		})
		require.Error(t, err)

		def2, err := llb.Image("busybox:latest").Marshal(ctx)
		require.NoError(t, err)

		res2, err := c.Solve(ctx, client.SolveRequest{
			Definition: def2.ToPB(),
		})
		require.NoError(t, err)

		ref2, err := res2.SingleRef()
		require.NoError(t, err)

		// Second stat should not error because the rootfs for `busybox` should not
		// have been released.
		_, err = ref2.StatFile(ctx, client.StatRequest{
			Path: ".",
		})
		require.NoError(t, err)

		return res2, nil
	}

	_, err = c.Build(ctx, SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)
}

func testNoBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := gatewayapi.NewLLBBridgeClient(c.conn)
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no buildid found in context")
}

func testUnknownBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := c.gatewayClientForBuild(t.Name() + identity.NewID())
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such job")
	require.Equal(t, codes.NotFound, grpcerrors.Code(err))
}

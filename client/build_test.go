package client

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/util/testutil"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestClientGatewayIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testClientGatewaySolve,
		testClientGatewayFailedSolve,
		testClientGatewayEmptySolve,
		testNoBuildID,
		testUnknownBuildID,
	}, integration.WithMirroredImages(integration.OfficialImages("busybox:latest")))
}

func testClientGatewaySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := testutil.GetContext(t)

	c, err := newClient(ctx, sb.Address())
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

	tmpdir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

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

	read, err := ioutil.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, testStr, string(read))

	checkAllReleasable(t, c, sb, true)
}

func testClientGatewayFailedSolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := testutil.GetContext(t)

	c, err := newClient(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		return nil, errors.New("expected to fail")
	}

	_, err = c.Build(ctx, SolveOpt{}, "", b, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected to fail")
}

func testClientGatewayEmptySolve(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := testutil.GetContext(t)

	c, err := newClient(ctx, sb.Address())
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

func testNoBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := testutil.GetContext(t)

	c, err := newClient(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := gatewayapi.NewLLBBridgeClient(c.conn)
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no buildid found in context")
}

func testUnknownBuildID(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := testutil.GetContext(t)

	c, err := newClient(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	g := c.gatewayClientForBuild(t.Name() + identity.NewID())
	_, err = g.Ping(ctx, &gatewayapi.PingRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such job")
}

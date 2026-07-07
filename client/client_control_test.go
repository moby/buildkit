package client

import (
	"context"
	"fmt"
	"io"
	"testing"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func testCallInfo(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureInfo)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Info(sb.Context())
	require.NoError(t, err)
}

func testClientCustomGRPCOpts(t *testing.T, sb integration.Sandbox) {
	var interceptedMethods []string
	intercept := func(
		ctx context.Context,
		method string,
		req,
		reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		interceptedMethods = append(interceptedMethods, method)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	c, err := New(sb.Context(), sb.Address(), WithGRPCDialOption(grpc.WithChainUnaryInterceptor(intercept)))
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	st := llb.Image(imgName)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	require.Contains(t, interceptedMethods, "/moby.buildkit.v1.Control/Solve")
}

func testListenBuildHistoryExcludesSoftDeletedRecords(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Create 3 completed builds so we have multiple history records.
	var buildRefs [3]string
	for i := range buildRefs {
		def, err := llb.Scratch().File(llb.Mkfile(fmt.Sprintf("file%d", i), 0o644, nil)).Marshal(sb.Context())
		require.NoError(t, err)

		buildRefs[i] = identity.NewID()
		_, err = c.Solve(sb.Context(), def, SolveOpt{Ref: buildRefs[i]}, nil)
		require.NoError(t, err)
	}

	refToDelete := buildRefs[1]

	// Start a streaming listener on one specific ref. This increments the
	// internal reference count, which causes Delete to soft-delete instead
	// of removing the record from the database.
	listenerCtx, listenerCancel := context.WithCancelCause(sb.Context())
	defer listenerCancel(nil)

	cl, err := c.ControlClient().ListenBuildHistory(listenerCtx, &controlapi.BuildHistoryRequest{
		Ref: refToDelete,
	})
	require.NoError(t, err)

	// Read the initial record so the listener is fully registered.
	_, err = cl.Recv()
	require.NoError(t, err)

	// Soft-delete: the record is marked deleted but stays in the DB because
	// the listener above still holds a reference.
	_, err = c.ControlClient().UpdateBuildHistory(sb.Context(), &controlapi.UpdateBuildHistoryRequest{
		Ref:    refToDelete,
		Delete: true,
	})
	require.NoError(t, err)

	// List all remaining history records with a limit to trigger sorting.
	// Before the fix this panicked with a nil pointer dereference because
	// the soft-deleted record left a nil entry in the event slice.
	cl2, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		EarlyExit: true,
		Limit:     10,
	})
	require.NoError(t, err)

	gotRefs := map[string]bool{}
	for {
		resp, err := cl2.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		gotRefs[resp.Record.Ref] = true
	}

	// The soft-deleted record must not appear in the results.
	assert.False(t, gotRefs[refToDelete], "soft-deleted ref %s should not appear in history list", refToDelete)
	assert.True(t, gotRefs[buildRefs[0]], "ref %s should appear in history list", buildRefs[0])
	assert.True(t, gotRefs[buildRefs[2]], "ref %s should appear in history list", buildRefs[2])

	// Clean up the streaming listener.
	listenerCancel(nil)
	// Drain the stream so gRPC can clean up.
	for {
		if _, err := cl.Recv(); err != nil {
			break
		}
	}
}

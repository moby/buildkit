package client

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"testing"

	"github.com/moby/buildkit/client/llb"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	sourcepolicpb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testSourcePolicySession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type tcase struct {
		name          string
		state         func() llb.State
		callbacks     []policysession.PolicyCallback
		expectedError string
	}

	tcases := []tcase{
		{
			name:  "basic alpine",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, runtime.GOOS, req.Platform.OS)
					require.Equal(t, runtime.GOARCH, req.Platform.Architecture)

					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return &policysession.DecisionResponse{
						Action: sourcepolicpb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "alpine with attrs",
			state: func() llb.State { return llb.Image("alpine", llb.WithLayerLimit(1)) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Equal(t, map[string]string{
						"image.layerlimit": "1",
					}, req.Source.Source.Attrs)
					return &policysession.DecisionResponse{
						Action: sourcepolicpb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "deny alpine",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return nil, nil, errors.New("policy denied")
				},
			},
			expectedError: "policy denied",
		},
		{
			name:  "alpine with digest policy",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
						// TODO: resolveMode
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.NotEmpty(t, req.Source.Image.Digest)
					_, err := digest.Parse(req.Source.Image.Digest)
					require.NoError(t, err)
					require.NotEmpty(t, req.Source.Image.Config)
					var cfg ocispecs.Image
					err = json.Unmarshal(req.Source.Image.Config, &cfg)
					require.NoError(t, err)
					require.NotEmpty(t, cfg.RootFS)
					return &policysession.DecisionResponse{
						Action: sourcepolicpb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.state()
			def, err := st.Marshal(ctx)
			require.NoError(t, err)

			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})

			_, err = c.Solve(ctx, def, SolveOpt{
				SourcePolicyProvider: p,
				Exports: []ExportEntry{
					{
						Type:   ExporterOCI,
						Output: fixedWriteCloser(nopWriteCloser{io.Discard}),
					},
				},
			}, nil)
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}
			require.NoError(t, err)

			require.Equal(t, len(tc.callbacks), callCounter, "not all policy callbacks were called")
		})
	}
}

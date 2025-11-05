package client

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	client "github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	opspb "github.com/moby/buildkit/solver/pb"
	sourcepolicpb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/testutil/integration"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testSourcePolicySession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

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

func testSourceMetaPolicySession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	type tcase struct {
		name          string
		source        func() (*opspb.SourceOp, sourceresolver.Opt)
		callbacks     []policysession.PolicyCallback
		expectedError string
	}
	tcases := []tcase{
		{
			name: "basic alpine",
			source: func() (*opspb.SourceOp, sourceresolver.Opt) {
				p := platforms.DefaultSpec()
				return &opspb.SourceOp{
						Identifier: "docker-image://docker.io/library/alpine:latest",
					}, sourceresolver.Opt{
						ImageOpt: &sourceresolver.ResolveImageOpt{
							Platform: &p,
						},
					}
			},
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
			name: "alpine denied",
			source: func() (*opspb.SourceOp, sourceresolver.Opt) {
				return &opspb.SourceOp{
					Identifier: "docker-image://docker.io/library/alpine:latest",
				}, sourceresolver.Opt{}
			},
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					return nil, nil, errors.New("policy denied")
				},
			},
			expectedError: "policy denied",
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			callCounter := 0

			p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
				if callCounter >= len(tc.callbacks) {
					return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
				}
				cb := tc.callbacks[callCounter]
				callCounter++
				return cb(ctx, req)
			})
			_, err = c.Build(ctx, SolveOpt{
				SourcePolicyProvider: p,
			}, "test", func(ctx context.Context, c client.Client) (*client.Result, error) {
				sop, opts := tc.source()
				_, err = c.ResolveSourceMetadata(ctx, sop, opts)
				return nil, err
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

func testSourcePolicyParallelSession(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("alpine").File(llb.Copy(llb.Image("busybox"), "/etc/passwd", "passwd2")).Marshal(ctx)
	require.NoError(t, err)

	countAlpine := 0
	countBusybox := 0
	waitBusyboxStart := make(chan struct{})
	waitAlpineDone := make(chan struct{})

	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		switch req.Source.Source.Identifier {
		case "docker-image://docker.io/library/alpine:latest":
			switch countAlpine {
			case 0:
				<-waitBusyboxStart
				require.Nil(t, req.Source.Image)
				countAlpine++
				return nil, &pb.ResolveSourceMetaRequest{
					Source:   req.Source.Source,
					Platform: req.Platform,
				}, nil
			case 1:
				require.NotNil(t, req.Source.Image)
				require.True(t, strings.HasPrefix(req.Source.Image.Digest, "sha256:"))
				countAlpine++
				close(waitAlpineDone)
				return &policysession.DecisionResponse{
					Action: sourcepolicpb.PolicyAction_ALLOW,
				}, nil, nil
			default:
				require.Fail(t, "too many calls for alpine")
			}
		case "docker-image://docker.io/library/busybox:latest":
			time.Sleep(200 * time.Millisecond)
			close(waitBusyboxStart)
			countBusybox++
			<-waitAlpineDone
			return &policysession.DecisionResponse{
				Action: sourcepolicpb.PolicyAction_ALLOW,
			}, nil, nil
		}
		return nil, nil, errors.Errorf("unexpected source %q", req.Source.Source.Identifier)
	})

	_, err = c.Solve(ctx, def, SolveOpt{
		SourcePolicyProvider: p,
	}, nil)
	require.NoError(t, err)

	require.Equal(t, 2, countAlpine)
	require.Equal(t, 1, countBusybox)
}

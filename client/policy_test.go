package client

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"hash"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/containerd/platforms"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	opspb "github.com/moby/buildkit/solver/pb"
	sourcepolicypb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/pgpsign"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	policyimage "github.com/moby/policy-helpers/image"
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
						Action: sourcepolicypb.PolicyAction_ALLOW,
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
						Action: sourcepolicypb.PolicyAction_ALLOW,
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
						Action: sourcepolicypb.PolicyAction_ALLOW,
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

func testSourcePolicySessionDenyMessages(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	def, err := llb.Image("alpine").Marshal(ctx)
	require.NoError(t, err)

	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
		return &policysession.DecisionResponse{
			Action: sourcepolicypb.PolicyAction_DENY,
			DenyMessages: []*policysession.DenyMessage{
				{Message: "policy blocked alpine"},
				{Message: "use busybox instead"},
			},
		}, nil, nil
	})

	_, err = c.Solve(ctx, def, SolveOpt{
		SourcePolicyProvider: p,
	}, nil)
	require.Error(t, err)

	denyMessages := policysession.DenyMessages(err)
	require.Len(t, denyMessages, 2)
	require.Equal(t, "policy blocked alpine", denyMessages[0].GetMessage())
	require.Equal(t, "use busybox instead", denyMessages[1].GetMessage())
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
						Action: sourcepolicypb.PolicyAction_ALLOW,
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
			}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
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

func testSourceMetaPolicySessionResolveAttestations(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureProvenance)
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target, platform := buildProvenanceImage(ctx, t, c, sb)
	sourceID := "docker-image://" + target
	requestedPredicateType := policyimage.SLSAProvenancePredicateType1

	callbackCalls := 0
	p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
		switch callbackCalls {
		case 0:
			callbackCalls++
			require.Equal(t, sourceID, req.Source.Source.Identifier)
			require.Nil(t, req.Source.Image)
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				Image: &pb.ResolveSourceImageRequest{
					NoConfig:            true,
					ResolveAttestations: []string{requestedPredicateType},
				},
			}, nil
		case 1:
			callbackCalls++
			require.Equal(t, sourceID, req.Source.Source.Identifier)
			require.NotNil(t, req.Source.Image)
			require.Empty(t, req.Source.Image.Config)
			require.NotNil(t, req.Source.Image.AttestationChain)
			ac := req.Source.Image.AttestationChain
			require.NotEmpty(t, ac.AttestationManifest)

			att, ok := ac.Blobs[ac.AttestationManifest]
			require.True(t, ok)
			require.NotEmpty(t, att.Data)

			var manifest ocispecs.Manifest
			require.NoError(t, json.Unmarshal(att.Data, &manifest))
			require.NotEmpty(t, manifest.Layers)

			foundRequestedType := false

			imageManifestDigest, err := digest.Parse(ac.ImageManifest)
			require.NoError(t, err)

			for _, layer := range manifest.Layers {
				layerPredicateType := layer.Annotations["in-toto.io/predicate-type"]
				if layerPredicateType != requestedPredicateType {
					continue
				}
				foundRequestedType = true

				blob, ok := ac.Blobs[string(layer.Digest)]
				require.True(t, ok, "missing blob for requested predicate type %q", layerPredicateType)
				require.NotEmpty(t, blob.Data, "empty blob data for requested predicate type %q", layerPredicateType)

				var stmt intoto.Statement
				require.NoError(t, json.Unmarshal(blob.Data, &stmt))
				require.Equal(t, "https://in-toto.io/Statement/v0.1", stmt.Type)
				require.Equal(t, layerPredicateType, stmt.PredicateType)
				require.NotEmpty(t, stmt.Subject)
				require.Equal(t, imageManifestDigest.Hex(), stmt.Subject[0].Digest["sha256"])
			}
			require.True(t, foundRequestedType, "requested predicate type %q not found in attestation manifest layers", requestedPredicateType)

			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_ALLOW,
			}, nil, nil
		default:
			return nil, nil, errors.Errorf("too many policy callbacks: %d", callbackCalls)
		}
	})

	_, err = c.Build(ctx, SolveOpt{
		SourcePolicyProvider: p,
	}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		_, err := c.ResolveSourceMetadata(ctx, &opspb.SourceOp{
			Identifier: sourceID,
		}, sourceresolver.Opt{
			ImageOpt: &sourceresolver.ResolveImageOpt{
				Platform: &platform,
			},
		})
		return nil, err
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, callbackCalls)
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
					Action: sourcepolicypb.PolicyAction_ALLOW,
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
				Action: sourcepolicypb.PolicyAction_ALLOW,
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

func testSourcePolicySignedCommit(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	signFixturesPath, ok := os.LookupEnv("BUILDKIT_TEST_SIGN_FIXTURES")
	if !ok {
		t.Skip("missing BUILDKIT_TEST_SIGN_FIXTURES")
	}

	withSign := func(user, method string) []string {
		return []string{
			"GIT_CONFIG_GLOBAL=" + filepath.Join(signFixturesPath, user+"."+method+".gitconfig"),
		}
	}

	gitDir := t.TempDir()
	gitCommands := []string{
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo a > a",
		"git add a",
		"git commit -m a",
		"git tag -a v0.1 -m v0.1",
	}
	err = runInDir(gitDir, gitCommands...)
	require.NoError(t, err)
	gitCommands = []string{
		"echo b > b",
		"git add b",
		"git commit -m b",
		"git checkout -B v2",
	}
	err = runInDirEnv(gitDir, withSign("user1", "gpg"), gitCommands...)
	require.NoError(t, err)
	gitCommands = []string{
		"git tag -s -a v2.0 -m v2.0-tag",
		"git update-server-info",
	}
	err = runInDirEnv(gitDir, withSign("user2", "ssh"), gitCommands...)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	pubKeyUser1gpg, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.gpg.pub"))
	require.NoError(t, err)

	pubKeyUser2ssh, err := os.ReadFile(filepath.Join(signFixturesPath, "user2.ssh.pub"))
	require.NoError(t, err)

	type testCase struct {
		state       func() llb.State
		name        string
		srcPol      *sourcepolicypb.Policy
		expectedErr string
	}

	gitURL := "git://" + strings.TrimPrefix(server.URL, "http://") + "/.git"

	tests := []testCase{
		{
			name: "unsigned commit fails",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v0.1",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v0.1",
							Attrs: map[string]string{
								"git.sig.pubkey": string(pubKeyUser1gpg),
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v0.1"))
			},
			expectedErr: "git object is not signed",
		},
		{
			name: "valid gpg signature for branch",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2",
							Attrs: map[string]string{
								"git.sig.pubkey":          string(pubKeyUser1gpg),
								"git.sig.rejectexpired":   "true",
								"git.sig.ignoresignedtag": "false",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2"))
			},
		},
		{
			name: "valid ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser2ssh),
								"git.sig.requiresignedtag": "true",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "invalid ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser1gpg),
								"git.sig.requiresignedtag": "true",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			expectedErr: "failed to parse ssh public key",
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "commit ssh signature for signed tag",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":           string(pubKeyUser1gpg),
								"git.sig.requiresignedtag": "false",
								"git.sig.rejectexpired":    "true",
							},
						},
					},
				},
			},
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
		{
			name: "invalid tag signature for commit",
			srcPol: &sourcepolicypb.Policy{
				Rules: []*sourcepolicypb.Rule{
					{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Selector: &sourcepolicypb.Selector{
							Identifier: gitURL + "#v2.0",
						},
						Updates: &sourcepolicypb.Update{
							Identifier: gitURL + "#v2.0",
							Attrs: map[string]string{
								"git.sig.pubkey":          string(pubKeyUser2ssh),
								"git.sig.rejectexpired":   "true",
								"git.sig.ignoresignedtag": "true",
							},
						},
					},
				},
			},
			expectedErr: "failed to read armored public key",
			state: func() llb.State {
				return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				st := llb.Scratch().File(
					llb.Copy(tt.state(), "a", "/a2"),
				)
				def, err := st.Marshal(sb.Context())
				if err != nil {
					return nil, err
				}
				return c.Solve(ctx, gateway.SolveRequest{
					Definition: def.ToPB(),
				})
			}

			_, err := c.Build(sb.Context(), SolveOpt{
				SourcePolicy: tt.srcPol,
			}, "", frontend, nil)
			if tt.expectedErr == "" {
				require.NoError(t, err, "test case %q failed", tt.name)
				return
			}
			require.ErrorContains(t, err, tt.expectedErr, "test case %q failed", tt.name)
		})
	}

	// session policy based test cases

	type tcase struct {
		name          string
		state         func() llb.State
		callbacks     []policysession.PolicyCallback
		expectedError string
	}

	tcases := []tcase{
		{
			name:  "gitchecksum",
			state: func() llb.State { return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0")) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Git)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.NotNil(t, req.Source.Git)
					require.Len(t, req.Source.Git.Checksum, 40)
					require.Len(t, req.Source.Git.CommitChecksum, 40)
					require.NotEqual(t, req.Source.Git.Checksum, req.Source.Git.CommitChecksum)
					require.Nil(t, req.Source.Git.CommitObject)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "gitobjects",
			state: func() llb.State { return llb.Git(server.URL+"/.git", "", llb.GitRef("v2.0")) },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Git)
					return nil, &pb.ResolveSourceMetaRequest{
						Source:   req.Source.Source,
						Platform: req.Platform,
						Git: &pb.ResolveSourceGitRequest{
							ReturnObject: true,
						},
					}, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, gitURL+"#v2.0", req.Source.Source.Identifier)
					require.NotNil(t, req.Source.Git)
					require.Len(t, req.Source.Git.Checksum, 40)
					require.Len(t, req.Source.Git.CommitChecksum, 40)
					require.NotEqual(t, req.Source.Git.Checksum, req.Source.Git.CommitChecksum)
					require.NotNil(t, req.Source.Git.CommitObject)
					require.Greater(t, len(req.Source.Git.CommitObject), 50)
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
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

func testSourcePolicySessionConvert(t *testing.T, sb integration.Sandbox) {
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
			name:  "convert and allow",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					src := req.Source.Source
					src.Identifier = "docker-image://docker.io/library/busybox:latest"
					if src.Attrs == nil {
						src.Attrs = map[string]string{}
					}
					src.Attrs["foo"] = "bar"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/busybox:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "bar", req.Source.Source.Attrs["foo"])
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_ALLOW,
					}, nil, nil
				},
			},
		},
		{
			name:  "convert and deny",
			state: func() llb.State { return llb.Image("alpine") },
			callbacks: []policysession.PolicyCallback{
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					src := req.Source.Source
					if src.Attrs == nil {
						src.Attrs = map[string]string{}
					}
					src.Attrs["foo"] = "bar"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "bar", req.Source.Source.Attrs["foo"])
					src := req.Source.Source
					src.Attrs["foo"] = "baz"
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_CONVERT,
						Update: src,
					}, nil, nil
				},
				func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
					require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
					require.Nil(t, req.Source.Image)
					require.Equal(t, "baz", req.Source.Source.Attrs["foo"])
					return &policysession.DecisionResponse{
						Action: sourcepolicypb.PolicyAction_DENY,
					}, nil, nil
				},
			},
			expectedError: "not allowed by policy",
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

	// policy loop test
	t.Run("convert loop", func(t *testing.T) {
		def, err := llb.Image("alpine").Marshal(ctx)
		require.NoError(t, err)

		calls := 0

		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			require.Equal(t, "docker-image://docker.io/library/alpine:latest", req.Source.Source.Identifier)
			require.Nil(t, req.Source.Image)
			calls++
			return &policysession.DecisionResponse{
				Action: sourcepolicypb.PolicyAction_CONVERT,
				Update: req.Source.Source,
			}, nil, nil
		})
		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.ErrorContains(t, err, "too many policy requests")
		require.Equal(t, 10, calls) // this is not strict value but just to make sure calls happened. future version may optimize this with less calls.
	})
}

func testSourcePolicySessionHTTPChecksumAssist(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	signFixturesPath, ok := os.LookupEnv("BUILDKIT_TEST_SIGN_FIXTURES")
	if !ok {
		t.Skip("missing BUILDKIT_TEST_SIGN_FIXTURES")
	}

	payload, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact"))
	require.NoError(t, err)
	sigData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.http.artifact.asc"))
	require.NoError(t, err)
	pubKeyData, err := os.ReadFile(filepath.Join(signFixturesPath, "user1.gpg.pub"))
	require.NoError(t, err)
	sig, _, err := pgpsign.ParseArmoredDetachedSignature(sigData)
	require.NoError(t, err)
	keyring, err := pgpsign.ReadAllArmoredKeyRings(pubKeyData)
	require.NoError(t, err)

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifact.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer httpSrv.Close()

	def, err := llb.Scratch().File(llb.Copy(llb.HTTP(httpSrv.URL+"/artifact.txt"), "artifact.txt", "/artifact.txt")).Marshal(ctx)
	require.NoError(t, err)

	t.Run("valid checksum request", func(t *testing.T) {
		algo, err := toPBChecksumAlgo(sig.Hash)
		require.NoError(t, err)
		expectedDigest, err := payloadWithSuffixDigest(sig.Hash, payload, sig.HashSuffix)
		require.NoError(t, err)

		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			switch callCounter {
			case 0:
				callCounter++
				return nil, &pb.ResolveSourceMetaRequest{
					Source:   req.Source.Source,
					Platform: req.Platform,
					HTTP: &pb.ResolveSourceHTTPRequest{
						ChecksumRequest: &pb.ChecksumRequest{
							Algo:   algo,
							Suffix: slices.Clone(sig.HashSuffix),
						},
					},
				}, nil
			case 1:
				callCounter++
				require.NotNil(t, req.Source.HTTP)
				require.NotNil(t, req.Source.HTTP.ChecksumResponse)
				require.Equal(t, expectedDigest, req.Source.HTTP.ChecksumResponse.Digest)
				require.Equal(t, sig.HashSuffix, req.Source.HTTP.ChecksumResponse.Suffix)
				responseDigest, err := digest.Parse(req.Source.HTTP.ChecksumResponse.Digest)
				require.NoError(t, err)
				require.NoError(t, pgpsign.VerifySignatureWithDigest(sig, keyring, responseDigest))
				// Negative check: tampered digest must fail signature verification.
				badDigest := tamperDigestHex(responseDigest)
				err = pgpsign.VerifySignatureWithDigest(sig, keyring, badDigest)
				require.Error(t, err)
				require.ErrorContains(t, err, "failed to verify signature with checksum digest")
				return &policysession.DecisionResponse{
					Action: sourcepolicypb.PolicyAction_ALLOW,
				}, nil, nil
			default:
				return nil, nil, errors.Errorf("too many calls to policy callback %d", callCounter)
			}
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.NoError(t, err)
		require.Equal(t, 2, callCounter)
	})

	t.Run("oversized suffix denied", func(t *testing.T) {
		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			callCounter++
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				HTTP: &pb.ResolveSourceHTTPRequest{
					ChecksumRequest: &pb.ChecksumRequest{
						Algo:   pb.ChecksumRequest_CHECKSUM_ALGO_SHA256,
						Suffix: make([]byte, 4097),
					},
				},
			}, nil
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "suffix exceeds max size")
		require.Equal(t, 1, callCounter)
	})

	t.Run("unsupported algo denied", func(t *testing.T) {
		callCounter := 0
		p := policysession.NewPolicyProvider(func(ctx context.Context, req *policysession.CheckPolicyRequest) (*policysession.DecisionResponse, *pb.ResolveSourceMetaRequest, error) {
			callCounter++
			return nil, &pb.ResolveSourceMetaRequest{
				Source:   req.Source.Source,
				Platform: req.Platform,
				HTTP: &pb.ResolveSourceHTTPRequest{
					ChecksumRequest: &pb.ChecksumRequest{
						Algo:   pb.ChecksumRequest_ChecksumAlgo(99),
						Suffix: []byte{1, 2, 3},
					},
				},
			}, nil
		})

		_, err = c.Solve(ctx, def, SolveOpt{
			SourcePolicyProvider: p,
		}, nil)
		require.Error(t, err)
		require.ErrorContains(t, err, "unsupported checksum algorithm")
		require.Equal(t, 1, callCounter)
	})
}

func toPBChecksumAlgo(in crypto.Hash) (pb.ChecksumRequest_ChecksumAlgo, error) {
	switch in {
	case crypto.SHA256:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA256, nil
	case crypto.SHA384:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA384, nil
	case crypto.SHA512:
		return pb.ChecksumRequest_CHECKSUM_ALGO_SHA512, nil
	default:
		return 0, errors.Errorf("unsupported signature hash algorithm %v", in)
	}
}

func payloadWithSuffixDigest(algo crypto.Hash, payload, suffix []byte) (string, error) {
	var (
		h        hash.Hash
		algoName string
	)
	switch algo {
	case crypto.SHA256:
		h = sha256.New()
		algoName = "sha256"
	case crypto.SHA384:
		h = sha512.New384()
		algoName = "sha384"
	case crypto.SHA512:
		h = sha512.New()
		algoName = "sha512"
	default:
		return "", errors.Errorf("unsupported signature hash algorithm %v", algo)
	}
	if _, err := h.Write(payload); err != nil {
		return "", err
	}
	if _, err := h.Write(suffix); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%x", algoName, h.Sum(nil)), nil
}

func tamperDigestHex(dgst digest.Digest) digest.Digest {
	hexPart := []byte(dgst.Encoded())
	if len(hexPart) == 0 {
		return dgst
	}
	if hexPart[len(hexPart)-1] == '0' {
		hexPart[len(hexPart)-1] = '1'
	} else {
		hexPart[len(hexPart)-1] = '0'
	}
	return digest.NewDigestFromEncoded(dgst.Algorithm(), string(hexPart))
}

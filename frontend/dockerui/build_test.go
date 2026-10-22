package dockerui

import (
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestNormalizePlatform(t *testing.T) {
	testCases := []struct {
		p, imgP  ocispecs.Platform
		expected exptypes.Platform
	}{
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64", // Not "linux/arm64/v8" https://github.com/moby/buildkit/issues/5915
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64",
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			expected: exptypes.Platform{
				ID: "windows/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "11.0.22000.0",
			},
			expected: exptypes.Platform{
				ID: "windows(10.0.19041.0)/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
	}

	for _, tc := range testCases {
		require.Equal(t, tc.expected, makeExportPlatform(tc.p, tc.imgP))
		// the ID needs to always be formatall(normalize(p))
		require.Equal(t, platforms.FormatAll(platforms.Normalize(tc.p)), tc.expected.ID)
	}
}

func TestGitDebugCommandsBuildArg(t *testing.T) {
	for _, value := range []string{"", "false", "1", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			bc := &Client{bopts: client.BuildOpts{Opts: map[string]string{
				"build-arg:BUILDKIT_GIT_DEBUG_COMMANDS": value,
			}}}
			err := bc.init()
			if value == "invalid" {
				require.ErrorContains(t, err, "failed to parse build-arg:BUILDKIT_GIT_DEBUG_COMMANDS")
				return
			}
			require.NoError(t, err)
			require.Equal(t, value == "1" || value == "true", bc.GitDebugCommands)
		})
	}
}

func TestDetectGitContextForwardsDebugCommands(t *testing.T) {
	t.Parallel()

	st, ok, err := DetectGitContext("https://github.com/docker/buildx.git?ref=refs/pull/3732/merge", nil, llb.GitDebugCommands(true))
	require.True(t, ok)
	require.NoError(t, err)

	g, err := sourceOpFromState(t.Context(), st)
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, "git://github.com/docker/buildx.git#refs/pull/3732/merge", g.Identifier)
	require.Equal(t, map[string]string{
		"git.authheadersecret": "GIT_AUTH_HEADER",
		"git.authtokensecret":  "GIT_AUTH_TOKEN",
		"git.debugcommands":    "true",
		"git.fullurl":          "https://github.com/docker/buildx.git",
	}, g.Attrs)
}

func TestGitContextDebugCommands(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		t.Run(value, func(t *testing.T) {
			bc := &Client{bopts: client.BuildOpts{
				Opts: map[string]string{
					"build-arg:BUILDKIT_GIT_DEBUG_COMMANDS": value,
				},
				Caps: pb.Caps.CapSet(pb.Caps.All()),
			}}
			require.NoError(t, bc.init())
			for _, ref := range []string{"git://example.com/repo.git", "http://example.com/repo.git", "https://example.com/repo.git"} {
				t.Run(ref, func(t *testing.T) {
					bc.bopts.Opts[DefaultLocalNameContext] = ref
					bctx, err := bc.initContext(t.Context())
					require.NoError(t, err)
					require.NotNil(t, bctx.sourceOp)
					nc := &NamedContext{bc: bc, input: ref}
					st, _, err := nc.Load(t.Context())
					require.NoError(t, err)
					require.NotNil(t, st)
					op, err := sourceOpFromState(t.Context(), st)
					require.NoError(t, err)
					require.NotNil(t, op)
					for _, source := range []*pb.SourceOp{bctx.sourceOp, op} {
						require.Equal(t, value == "true", source.Attrs[pb.AttrGitDebugCommands] == "true")
					}
				})
			}
		})
	}
}

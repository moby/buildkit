package dockerui

import (
	"context"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
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

func TestDetectGitContextForwardsDebugCommands(t *testing.T) {
	t.Parallel()

	enabled := true
	st, ok, err := DetectGitContext("https://github.com/docker/buildx.git?ref=refs/pull/3732/merge", nil, &enabled)
	require.True(t, ok)
	require.NoError(t, err)

	g := marshalGitContext(t, st)
	require.Equal(t, "git://github.com/docker/buildx.git#refs/pull/3732/merge", g.Identifier)
	require.Equal(t, map[string]string{
		"git.authheadersecret": "GIT_AUTH_HEADER",
		"git.authtokensecret":  "GIT_AUTH_TOKEN",
		"git.debugcommands":    "true",
		"git.fullurl":          "https://github.com/docker/buildx.git",
	}, g.Attrs)
}

func marshalGitContext(t *testing.T, st *llb.State) *pb.SourceOp {
	t.Helper()

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 2, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[0])

	return arr[0].Op.(*pb.Op_Source).Source
}

func parseDef(t *testing.T, def [][]byte) (map[string]*pb.Op, []*pb.Op) {
	t.Helper()

	m := map[string]*pb.Op{}
	arr := make([]*pb.Op, 0, len(def))

	for _, dt := range def {
		var op pb.Op
		err := op.Unmarshal(dt)
		require.NoError(t, err)
		dgst := digest.FromBytes(dt)
		m[string(dgst)] = &op
		arr = append(arr, &op)
	}

	return m, arr
}

func last(t *testing.T, arr []*pb.Op) (string, int) {
	t.Helper()

	require.Greater(t, len(arr), 1)

	op := arr[len(arr)-1]
	require.Equal(t, 1, len(op.Inputs))
	return op.Inputs[0].Digest, int(op.Inputs[0].Index)
}

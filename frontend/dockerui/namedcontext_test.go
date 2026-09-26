package dockerui

import (
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestNamedContextGitURL(t *testing.T) {
	for _, tc := range []struct {
		input  string
		remote string
		ref    string
	}{
		{
			// llb.Git scans the host keys of SSH remotes, so use a local port
			// that refuses the connection instead of reaching the network.
			input:  "ssh://git@127.0.0.1:1/moby/buildkit.git#v0.20.0",
			remote: "ssh://git@127.0.0.1:1/moby/buildkit.git",
			ref:    "v0.20.0",
		},
		{
			input:  "git://github.com/moby/buildkit.git#v0.20.0",
			remote: "git://github.com/moby/buildkit.git",
			ref:    "v0.20.0",
		},
		{
			input:  "https://github.com/moby/buildkit.git#v0.20.0",
			remote: "https://github.com/moby/buildkit.git",
			ref:    "v0.20.0",
		},
	} {
		t.Run(tc.input, func(t *testing.T) {
			nc := &NamedContext{
				input:            tc.input,
				bc:               &Client{},
				name:             "src",
				nameWithPlatform: "src",
			}
			st, img, err := nc.Load(t.Context())
			require.NoError(t, err)
			require.Nil(t, img)
			require.NotNil(t, st)

			def, err := st.Marshal(t.Context())
			require.NoError(t, err)

			var src *pb.SourceOp
			for _, dt := range def.Def {
				var op pb.Op
				require.NoError(t, op.Unmarshal(dt))
				if s := op.GetSource(); s != nil {
					src = s
				}
			}
			require.NotNil(t, src)
			require.Equal(t, tc.remote, src.Attrs[pb.AttrFullRemoteURL])
			require.True(t, strings.HasSuffix(src.Identifier, "#"+tc.ref), src.Identifier)
		})
	}
}

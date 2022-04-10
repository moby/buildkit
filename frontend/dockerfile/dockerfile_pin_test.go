package dockerfile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	pintypes "github.com/moby/buildkit/util/pin/types"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var pinTests = integration.TestFuncs(
	testPin,
)

func init() {
	allTests = append(allTests, pinTests...)
}

func testPin(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := `
FROM alpine
ADD https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md /README.md
`
	type testCase struct {
		title         string
		dockerfilePin string
		valid         bool
		validateResp  func(*testing.T, *client.SolveResponse)
	}
	testCases := []testCase{
		{
			title: "Valid",
			dockerfilePin: `{
    "sources": [
      {
        "type": "docker-image",
        "ref": "docker.io/library/alpine:latest",
        "pin": "sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454"
      },
      {
        "type": "http",
        "ref": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
        "pin": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"
      }
    ]
}
`,
			valid: true,
			validateResp: func(t *testing.T, resp *client.SolveResponse) {
				require.Contains(t, resp.ExporterResponse, exptypes.ExporterPinConsumed)
				dtpc, err := base64.StdEncoding.DecodeString(resp.ExporterResponse[exptypes.ExporterPinConsumed])
				require.NoError(t, err)
				var pc pintypes.Consumed
				err = json.Unmarshal(dtpc, &pc)
				require.NoError(t, err)
				require.Len(t, pc.Sources, 2)
				require.Equal(t, "sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454", pc.Sources[0].Pin)
				require.Equal(t, "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53", pc.Sources[1].Pin)
			},
		},
		{
			title: "InvalidDockerImagePin",
			dockerfilePin: `{
    "sources": [
      {
        "type": "docker-image",
        "ref": "docker.io/library/alpine:latest",
        "pin": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
      },
      {
        "type": "http",
        "ref": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
        "pin": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"
      }
    ]
}
`,
			valid: false,
		},
		{
			title: "InvalidHTTPPin",
			dockerfilePin: `{
    "sources": [
      {
        "type": "docker-image",
        "ref": "docker.io/library/alpine:latest",
        "pin": "sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454"
      },
      {
        "type": "http",
        "ref": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
        "pin": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
      }
    ]
}
`,
			valid: false,
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			dir, err := tmpdir(
				fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
				fstest.CreateFile("Dockerfile.pin", []byte(tc.dockerfilePin), 0600),
			)
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			c, err := client.New(sb.Context(), sb.Address())
			require.NoError(t, err)
			defer c.Close()

			res, err := f.Solve(sb.Context(), c, client.SolveOpt{
				LocalDirs: map[string]string{
					builder.DefaultLocalNameDockerfile: dir,
					builder.DefaultLocalNameContext:    dir,
				},
			}, nil)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.validateResp != nil {
				tc.validateResp(t, res)
			}
		})
	}
}

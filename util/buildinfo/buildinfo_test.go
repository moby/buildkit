package buildinfo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecode(t *testing.T) {
	bi, err := Decode("eyJzb3VyY2VzIjpbeyJ0eXBlIjoiaW1hZ2UiLCJyZWYiOiJkb2NrZXIuaW8vZG9ja2VyL2J1aWxkeC1iaW46MC42LjFAc2hhMjU2OmE2NTJjZWQ0YTQxNDE5NzdjN2RhYWVkMGEwNzRkY2Q5ODQ0YTc4ZDdkMjYxNTQ2NWIxMmY0MzNhZTZkZDI5ZjAiLCJwaW4iOiJzaGEyNTY6YTY1MmNlZDRhNDE0MTk3N2M3ZGFhZWQwYTA3NGRjZDk4NDRhNzhkN2QyNjE1NDY1YjEyZjQzM2FlNmRkMjlmMCJ9LHsidHlwZSI6ImltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2xpYnJhcnkvYWxwaW5lOjMuMTMiLCJwaW4iOiJzaGEyNTY6MWQzMGQxYmEzY2I5MDk2MjA2N2U5YjI5NDkxZmJkNTY5OTc5NzlkNTQzNzZmMjNmMDE0NDhiNWM1Y2Q4YjQ2MiJ9LHsidHlwZSI6ImltYWdlIiwicmVmIjoiZG9ja2VyLmlvL21vYnkvYnVpbGRraXQ6djAuOS4wIiwicGluIjoic2hhMjU2OjhkYzY2OGU3ZjY2ZGIxYzA0NGFhZGJlZDMwNjAyMDc0MzUxNmE5NDg0ODc5M2UwZjgxZjk0YTA4N2VlNzhjYWIifSx7InR5cGUiOiJpbWFnZSIsInJlZiI6ImRvY2tlci5pby90b25pc3RpaWdpL3h4QHNoYTI1NjoyMWE2MWJlNDc0NGY2NTMxY2I1ZjMzYjBlNmY0MGVkZTQxZmEzYTFiOGM4MmQ1OTQ2MTc4ZjgwY2M4NGJmYzA0IiwicGluIjoic2hhMjU2OjIxYTYxYmU0NzQ0ZjY1MzFjYjVmMzNiMGU2ZjQwZWRlNDFmYTNhMWI4YzgyZDU5NDYxNzhmODBjYzg0YmZjMDQifSx7InR5cGUiOiJnaXQiLCJyZWYiOiJodHRwczovL2dpdGh1Yi5jb20vY3JhenktbWF4L2J1aWxka2l0LWJ1aWxkc291cmNlcy10ZXN0LmdpdCNtYXN0ZXIiLCJwaW4iOiIyNTlhNWFhNWFhNWJiMzU2MmQxMmNjNjMxZmUzOTlmNDc4ODY0MmMxIn0seyJ0eXBlIjoiaHR0cCIsInJlZiI6Imh0dHBzOi8vcmF3LmdpdGh1YnVzZXJjb250ZW50LmNvbS9tb2J5L21vYnkvbWFzdGVyL1JFQURNRS5tZCIsInBpbiI6InNoYTI1Njo0MTk0NTUyMDJiMGVmOTdlNDgwZDdmODE5OWIyNmE3MjFhNDE3ODE4YmMwZTJkMTA2OTc1Zjc0MzIzZjI1ZTZjIn1dfQ==")
	require.NoError(t, err)
	assert.Equal(t, 6, len(bi.Sources))
}

func TestMerge(t *testing.T) {
	buildSourcesLLB := map[string]string{
		"docker-image://docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0": "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
		"docker-image://docker.io/library/alpine:3.13@sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462":     "sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462",
		"docker-image://docker.io/moby/buildkit:v0.9.0@sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab":    "sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab",
		"docker-image://docker.io/tonistiigi/xx@sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04":           "sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
		"git://https://github.com/crazy-max/buildkit-buildsources-test.git#master":                                                 "259a5aa5aa5bb3562d12cc631fe399f4788642c1",
		"https://raw.githubusercontent.com/moby/moby/master/README.md":                                                             "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
	}

	buildInfoImageConfig, err := json.Marshal(binfotypes.BuildInfo{
		Sources: []binfotypes.Source{
			{
				Type:  binfotypes.SourceTypeDockerImage,
				Ref:   "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
				Alias: "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
				Pin:   "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
			},
			{
				Type:  binfotypes.SourceTypeDockerImage,
				Ref:   "docker.io/library/alpine:3.13",
				Alias: "docker.io/library/alpine:3.13@sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462",
				Pin:   "sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462",
			},
			{
				Type:  binfotypes.SourceTypeDockerImage,
				Ref:   "docker.io/moby/buildkit:v0.9.0",
				Alias: "docker.io/moby/buildkit:v0.9.0@sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab",
				Pin:   "sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab",
			},
			{
				Type:  binfotypes.SourceTypeDockerImage,
				Ref:   "docker.io/tonistiigi/xx@sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
				Alias: "docker.io/tonistiigi/xx@sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
				Pin:   "sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
			},
		},
	})
	require.NoError(t, err)

	bic, err := json.Marshal(binfotypes.ImageConfig{
		BuildInfo: buildInfoImageConfig,
	})
	require.NoError(t, err)

	ret, err := Merge(context.Background(), buildSourcesLLB, bic)
	require.NoError(t, err)

	dec, err := Decode(base64.StdEncoding.EncodeToString(ret))
	require.NoError(t, err)

	assert.Equal(t, binfotypes.BuildInfo{
		Sources: []binfotypes.Source{
			{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
				Pin:  "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
			},
			{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  "docker.io/library/alpine:3.13",
				Pin:  "sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462",
			},
			{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  "docker.io/moby/buildkit:v0.9.0",
				Pin:  "sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab",
			},
			{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  "docker.io/tonistiigi/xx@sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
				Pin:  "sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
			},
			{
				Type: binfotypes.SourceTypeGit,
				Ref:  "https://github.com/crazy-max/buildkit-buildsources-test.git#master",
				Pin:  "259a5aa5aa5bb3562d12cc631fe399f4788642c1",
			},
			{
				Type: binfotypes.SourceTypeHTTP,
				Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
				Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
			},
		},
	}, dec)
}

package buildinfo

import (
	"context"
	"encoding/json"
	"testing"

	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecode(t *testing.T) {
	bi, err := Decode("eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJhdHRycyI6eyJidWlsZC1hcmc6QlVJTERLSVRfSU5MSU5FX0JVSUxESU5GT19BVFRSUyI6IjEiLCJjbWRsaW5lIjoiY3JhenltYXgvZG9ja2VyZmlsZTpidWlsZGF0dHJzIiwiY29udGV4dCI6Imh0dHBzOi8vZ2l0aHViLmNvbS9jcmF6eS1tYXgvYnVpbGRraXQtYnVpbGRzb3VyY2VzLXRlc3QuZ2l0I21hc3RlciIsImZpbGVuYW1lIjoiRG9ja2VyZmlsZSIsInNvdXJjZSI6ImNyYXp5bWF4L2RvY2tlcmZpbGU6YnVpbGRhdHRycyJ9LCJzb3VyY2VzIjpbeyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2RvY2tlci9idWlsZHgtYmluOjAuNi4xQHNoYTI1NjphNjUyY2VkNGE0MTQxOTc3YzdkYWFlZDBhMDc0ZGNkOTg0NGE3OGQ3ZDI2MTU0NjViMTJmNDMzYWU2ZGQyOWYwIiwicGluIjoic2hhMjU2OmE2NTJjZWQ0YTQxNDE5NzdjN2RhYWVkMGEwNzRkY2Q5ODQ0YTc4ZDdkMjYxNTQ2NWIxMmY0MzNhZTZkZDI5ZjAifSx7InR5cGUiOiJkb2NrZXItaW1hZ2UiLCJyZWYiOiJkb2NrZXIuaW8vbGlicmFyeS9hbHBpbmU6My4xMyIsInBpbiI6InNoYTI1NjowMjZmNzIxYWY0Y2YyODQzZTA3YmJhNjQ4ZTE1OGZiMzVlY2M4NzZkODIyMTMwNjMzY2M0OWY3MDdmMGZjODhjIn0seyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL21vYnkvYnVpbGRraXQ6djAuOS4wIiwicGluIjoic2hhMjU2OjhkYzY2OGU3ZjY2ZGIxYzA0NGFhZGJlZDMwNjAyMDc0MzUxNmE5NDg0ODc5M2UwZjgxZjk0YTA4N2VlNzhjYWIifSx7InR5cGUiOiJkb2NrZXItaW1hZ2UiLCJyZWYiOiJkb2NrZXIuaW8vdG9uaXN0aWlnaS94eEBzaGEyNTY6MjFhNjFiZTQ3NDRmNjUzMWNiNWYzM2IwZTZmNDBlZGU0MWZhM2ExYjhjODJkNTk0NjE3OGY4MGNjODRiZmMwNCIsInBpbiI6InNoYTI1NjoyMWE2MWJlNDc0NGY2NTMxY2I1ZjMzYjBlNmY0MGVkZTQxZmEzYTFiOGM4MmQ1OTQ2MTc4ZjgwY2M4NGJmYzA0In0seyJ0eXBlIjoiZ2l0IiwicmVmIjoiaHR0cHM6Ly9naXRodWIuY29tL2NyYXp5LW1heC9idWlsZGtpdC1idWlsZHNvdXJjZXMtdGVzdC5naXQjbWFzdGVyIiwicGluIjoiNDNhOGJmOWMzNTFhYmY2NGIwODY1YTZhMDU0OGExZGUxZGVkNDBhOCJ9LHsidHlwZSI6Imh0dHAiLCJyZWYiOiJodHRwczovL3Jhdy5naXRodWJ1c2VyY29udGVudC5jb20vbW9ieS9tb2J5L21hc3Rlci9SRUFETUUubWQiLCJwaW4iOiJzaGEyNTY6NDE5NDU1MjAyYjBlZjk3ZTQ4MGQ3ZjgxOTliMjZhNzIxYTQxNzgxOGJjMGUyZDEwNjk3NWY3NDMyM2YyNWU2YyJ9XX0=")
	require.NoError(t, err)
	assert.Equal(t, 5, len(bi.Attrs))
	assert.Equal(t, 6, len(bi.Sources))
}

func TestMergeSources(t *testing.T) {
	buildSourcesLLB := map[string]string{
		"docker-image://docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0": "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
		"docker-image://docker.io/library/alpine:3.13@sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462":     "sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462",
		"docker-image://docker.io/moby/buildkit:v0.9.0@sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab":    "sha256:8dc668e7f66db1c044aadbed306020743516a94848793e0f81f94a087ee78cab",
		"docker-image://docker.io/tonistiigi/xx@sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04":           "sha256:21a61be4744f6531cb5f33b0e6f40ede41fa3a1b8c82d5946178f80cc84bfc04",
		"git://https://github.com/crazy-max/buildkit-buildsources-test.git#master":                                                 "259a5aa5aa5bb3562d12cc631fe399f4788642c1",
		"https://raw.githubusercontent.com/moby/moby/master/README.md":                                                             "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
	}

	frontendSources := []binfotypes.Source{
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
	}

	srcs, err := mergeSources(context.Background(), buildSourcesLLB, frontendSources)
	require.NoError(t, err)

	assert.Equal(t, []binfotypes.Source{
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
	}, srcs)
}

func TestFormat(t *testing.T) {
	bi := binfotypes.BuildInfo{
		Frontend: "dockerfile.v0",
		Attrs: map[string]string{
			"build-arg:foo": "bar",
			"cmdline":       "crazymax/dockerfile:master",
			"context":       "https://github.com/crazy-max/buildkit-buildsources-test.git#master",
			"filename":      "Dockerfile",
			"platform":      "linux/amd64,linux/arm64",
			"source":        "crazymax/dockerfile:master",
		},
		Sources: []binfotypes.Source{
			{
				Type:  binfotypes.SourceTypeDockerImage,
				Ref:   "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
				Alias: "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
				Pin:   "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
			},
		},
	}

	cases := []struct {
		name       string
		formatopts FormatOpts
		want       binfotypes.BuildInfo
	}{
		{
			name:       "unchanged",
			formatopts: FormatOpts{RemoveAttrs: false},
			want:       bi,
		},
		{
			name:       "remove attrs",
			formatopts: FormatOpts{RemoveAttrs: true},
			want: binfotypes.BuildInfo{
				Frontend: "dockerfile.v0",
				Sources: []binfotypes.Source{
					{
						Type:  binfotypes.SourceTypeDockerImage,
						Ref:   "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
						Alias: "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
						Pin:   "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
					},
				},
			},
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dt, err := json.Marshal(bi)
			require.NoError(t, err)
			dt, err = Format(dt, tt.formatopts)
			require.NoError(t, err)
			var res binfotypes.BuildInfo
			err = json.Unmarshal(dt, &res)
			require.NoError(t, err)
			assert.Equal(t, tt.want, res)
		})
	}
}

func TestReduceMap(t *testing.T) {
	cases := []struct {
		name     string
		m1       map[string]string
		m2       map[string]string
		expected map[string]string
	}{
		{
			name:     "first",
			m1:       map[string]string{"foo": "bar", "abc": "def"},
			m2:       map[string]string{"bar": "foo", "abc": "ghi"},
			expected: map[string]string{"foo": "bar", "abc": "def", "bar": "foo"},
		},
		{
			name:     "last",
			m1:       map[string]string{"bar": "foo", "abc": "ghi"},
			m2:       map[string]string{"foo": "bar", "abc": "def"},
			expected: map[string]string{"bar": "foo", "abc": "ghi", "foo": "bar"},
		},
		{
			name:     "null1",
			m1:       nil,
			m2:       map[string]string{"foo": "bar", "abc": "def"},
			expected: map[string]string{"foo": "bar", "abc": "def"},
		},
		{
			name:     "null2",
			m1:       map[string]string{"foo": "bar", "abc": "def"},
			m2:       nil,
			expected: map[string]string{"foo": "bar", "abc": "def"},
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, reduceMap(tt.m2, tt.m1))
		})
	}
}

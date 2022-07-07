package buildinfo

import (
	"encoding/json"
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"
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

	srcs, err := mergeSources(buildSourcesLLB, frontendSources)
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

func TestDecodeDeps(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		attrs map[string]*string
		want  map[string]binfotypes.BuildInfo
	}{
		{
			name: "simple",
			key:  exptypes.ExporterBuildInfo,
			attrs: map[string]*string{
				"build-arg:bar":         stringPtr("foo"),
				"build-arg:foo":         stringPtr("bar"),
				"context:baseapp":       stringPtr("input:0-base"),
				"filename":              stringPtr("Dockerfile"),
				"input-metadata:0-base": stringPtr("{\"containerimage.buildinfo\":\"eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJhdHRycyI6eyJidWlsZC1hcmc6YmFyIjoiZm9vIiwiYnVpbGQtYXJnOmZvbyI6ImJhciIsImZpbGVuYW1lIjoiYmFzZWFwcC5Eb2NrZXJmaWxlIn0sInNvdXJjZXMiOlt7InR5cGUiOiJkb2NrZXItaW1hZ2UiLCJyZWYiOiJkb2NrZXIuaW8vbGlicmFyeS9idXN5Ym94OmxhdGVzdCIsInBpbiI6InNoYTI1NjphZmNjN2YxYWMxYjQ5ZGIzMTdhNzE5NmM5MDJlNjFjNmMzYzQ2MDdkNjM1OTllZTFhODJkNzAyZDI0OWEwY2NiIn1dfQ==\",\"containerimage.config\":\"eyJhcmNoaXRlY3R1cmUiOiJhbWQ2NCIsIm9zIjoibGludXgiLCJyb290ZnMiOnsidHlwZSI6ImxheWVycyIsImRpZmZfaWRzIjpbInNoYTI1NjpkMzE1MDVmZDUwNTBmNmI5NmNhMzI2OGQxZGI1OGZjOTFhZTU2MWRkZjE0ZWFhYmM0MWQ2M2VhMmVmOGMxYzZkIl19LCJoaXN0b3J5IjpbeyJjcmVhdGVkIjoiMjAyMi0wMi0wNFQyMToyMDoxMi4zMTg5MTc4MjJaIiwiY3JlYXRlZF9ieSI6Ii9iaW4vc2ggLWMgIyhub3ApIEFERCBmaWxlOjFjODUwN2UzZTliMjJiOTc3OGYyZWRiYjk1MDA2MWUwNmJkZTZhMWY1M2I2OWUxYzYxMDI1MDAyOWMzNzNiNzIgaW4gLyAifSx7ImNyZWF0ZWQiOiIyMDIyLTAyLTA0VDIxOjIwOjEyLjQ5Nzc5NDgwOVoiLCJjcmVhdGVkX2J5IjoiL2Jpbi9zaCAtYyAjKG5vcCkgIENNRCBbXCJzaFwiXSIsImVtcHR5X2xheWVyIjp0cnVlfSx7ImNyZWF0ZWRfYnkiOiJXT1JLRElSIC9zcmMiLCJjb21tZW50IjoiYnVpbGRraXQuZG9ja2VyZmlsZS52MCJ9XSwiY29uZmlnIjp7IkVudiI6WyJQQVRIPS91c3IvbG9jYWwvc2JpbjovdXNyL2xvY2FsL2JpbjovdXNyL3NiaW46L3Vzci9iaW46L3NiaW46L2JpbiJdLCJDbWQiOlsic2giXSwiV29ya2luZ0RpciI6Ii9zcmMiLCJPbkJ1aWxkIjpudWxsfX0=\"}"),
			},
			want: map[string]binfotypes.BuildInfo{
				"0-base": {
					Frontend: "dockerfile.v0",
					Attrs: map[string]*string{
						"build-arg:bar": stringPtr("foo"),
						"build-arg:foo": stringPtr("bar"),
						"filename":      stringPtr("baseapp.Dockerfile"),
					},
					Sources: []binfotypes.Source{
						{
							Type: binfotypes.SourceTypeDockerImage,
							Ref:  "docker.io/library/busybox:latest",
							Pin:  "sha256:afcc7f1ac1b49db317a7196c902e61c6c3c4607d63599ee1a82d702d249a0ccb",
						},
					},
				},
			},
		},
		{
			name: "multiplatform",
			key:  exptypes.ExporterBuildInfo + "/linux/amd64",
			attrs: map[string]*string{
				"context:base::linux/amd64":        stringPtr("input:base::linux/amd64"),
				"context:base::linux/arm64":        stringPtr("input:base::linux/arm64"),
				"dockerfilekey":                    stringPtr("dockerfile2"),
				"input-metadata:base::linux/amd64": stringPtr("{\"containerimage.buildinfo\":\"eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJzb3VyY2VzIjpbeyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2xpYnJhcnkvYWxwaW5lOmxhdGVzdCIsInBpbiI6InNoYTI1NjplN2Q4OGRlNzNkYjNkM2ZkOWIyZDYzYWE3ZjQ0N2ExMGZkMDIyMGI3Y2JmMzk4MDNjODAzZjJhZjliYTI1NmIzIn1dfQ==\",\"containerimage.config\":\"eyJhcmNoaXRlY3R1cmUiOiJhbWQ2NCIsIm9zIjoibGludXgiLCJyb290ZnMiOnsidHlwZSI6ImxheWVycyIsImRpZmZfaWRzIjpbInNoYTI1Njo4ZDNhYzM0ODk5OTY0MjNmNTNkNjA4N2M4MTE4MDAwNjI2M2I3OWYyMDZkM2ZkZWM5ZTY2ZjBlMjdjZWI4NzU5Il19LCJoaXN0b3J5IjpbeyJjcmVhdGVkIjoiMjAyMS0xMS0yNFQyMDoxOTo0MC4xOTk3MDA5NDZaIiwiY3JlYXRlZF9ieSI6Ii9iaW4vc2ggLWMgIyhub3ApIEFERCBmaWxlOjkyMzNmNmYyMjM3ZDc5NjU5YTk1MjFmN2UzOTBkZjIxN2NlYzQ5ZjFhOGFhM2ExMjE0N2JiY2ExOTU2YWNkYjkgaW4gLyAifSx7ImNyZWF0ZWQiOiIyMDIxLTExLTI0VDIwOjE5OjQwLjQ4MzM2NzU0NloiLCJjcmVhdGVkX2J5IjoiL2Jpbi9zaCAtYyAjKG5vcCkgIENNRCBbXCIvYmluL3NoXCJdIiwiZW1wdHlfbGF5ZXIiOnRydWV9LHsiY3JlYXRlZF9ieSI6IkFSRyBUQVJHRVRBUkNIIiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAiLCJlbXB0eV9sYXllciI6dHJ1ZX0seyJjcmVhdGVkX2J5IjoiRU5WIEZPTz1iYXItYW1kNjQiLCJjb21tZW50IjoiYnVpbGRraXQuZG9ja2VyZmlsZS52MCIsImVtcHR5X2xheWVyIjp0cnVlfSx7ImNyZWF0ZWRfYnkiOiJSVU4gfDEgVEFSR0VUQVJDSD1hbWQ2NCAvYmluL3NoIC1jIGVjaG8gXCJmb28gJFRBUkdFVEFSQ0hcIiBcdTAwM2UgL291dCAjIGJ1aWxka2l0IiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAifV0sImNvbmZpZyI6eyJFbnYiOlsiUEFUSD0vdXNyL2xvY2FsL3NiaW46L3Vzci9sb2NhbC9iaW46L3Vzci9zYmluOi91c3IvYmluOi9zYmluOi9iaW4iLCJGT089YmFyLWFtZDY0Il0sIkNtZCI6WyIvYmluL3NoIl0sIk9uQnVpbGQiOm51bGx9fQ==\"}"),
				"input-metadata:base::linux/arm64": stringPtr("{\"containerimage.buildinfo\":\"eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJzb3VyY2VzIjpbeyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2xpYnJhcnkvYWxwaW5lOmxhdGVzdCIsInBpbiI6InNoYTI1NjplN2Q4OGRlNzNkYjNkM2ZkOWIyZDYzYWE3ZjQ0N2ExMGZkMDIyMGI3Y2JmMzk4MDNjODAzZjJhZjliYTI1NmIzIn1dfQ==\",\"containerimage.config\":\"eyJhcmNoaXRlY3R1cmUiOiJhcm02NCIsIm9zIjoibGludXgiLCJyb290ZnMiOnsidHlwZSI6ImxheWVycyIsImRpZmZfaWRzIjpbInNoYTI1Njo4ZDNhYzM0ODk5OTY0MjNmNTNkNjA4N2M4MTE4MDAwNjI2M2I3OWYyMDZkM2ZkZWM5ZTY2ZjBlMjdjZWI4NzU5Il19LCJoaXN0b3J5IjpbeyJjcmVhdGVkIjoiMjAyMS0xMS0yNFQyMDoxOTo0MC4xOTk3MDA5NDZaIiwiY3JlYXRlZF9ieSI6Ii9iaW4vc2ggLWMgIyhub3ApIEFERCBmaWxlOjkyMzNmNmYyMjM3ZDc5NjU5YTk1MjFmN2UzOTBkZjIxN2NlYzQ5ZjFhOGFhM2ExMjE0N2JiY2ExOTU2YWNkYjkgaW4gLyAifSx7ImNyZWF0ZWQiOiIyMDIxLTExLTI0VDIwOjE5OjQwLjQ4MzM2NzU0NloiLCJjcmVhdGVkX2J5IjoiL2Jpbi9zaCAtYyAjKG5vcCkgIENNRCBbXCIvYmluL3NoXCJdIiwiZW1wdHlfbGF5ZXIiOnRydWV9LHsiY3JlYXRlZF9ieSI6IkFSRyBUQVJHRVRBUkNIIiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAiLCJlbXB0eV9sYXllciI6dHJ1ZX0seyJjcmVhdGVkX2J5IjoiRU5WIEZPTz1iYXItYXJtNjQiLCJjb21tZW50IjoiYnVpbGRraXQuZG9ja2VyZmlsZS52MCIsImVtcHR5X2xheWVyIjp0cnVlfSx7ImNyZWF0ZWRfYnkiOiJSVU4gfDEgVEFSR0VUQVJDSD1hcm02NCAvYmluL3NoIC1jIGVjaG8gXCJmb28gJFRBUkdFVEFSQ0hcIiBcdTAwM2UgL291dCAjIGJ1aWxka2l0IiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAifV0sImNvbmZpZyI6eyJFbnYiOlsiUEFUSD0vdXNyL2xvY2FsL3NiaW46L3Vzci9sb2NhbC9iaW46L3Vzci9zYmluOi91c3IvYmluOi9zYmluOi9iaW4iLCJGT089YmFyLWFybTY0Il0sIkNtZCI6WyIvYmluL3NoIl0sIk9uQnVpbGQiOm51bGx9fQ==\"}"),
				"platform":                         stringPtr("linux/amd64,linux/arm64"),
			},
			want: map[string]binfotypes.BuildInfo{
				"base": {
					Frontend: "dockerfile.v0",
					Attrs:    nil,
					Sources: []binfotypes.Source{
						{
							Type: binfotypes.SourceTypeDockerImage,
							Ref:  "docker.io/library/alpine:latest",
							Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
						},
					},
				},
			},
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			deps, err := decodeDeps(tt.key, tt.attrs)
			require.NoError(t, err)
			assert.Equal(t, tt.want, deps)
		})
	}
}

func TestDedupSources(t *testing.T) {
	cases := []struct {
		name    string
		sources []binfotypes.Source
		deps    map[string]binfotypes.BuildInfo
		want    []binfotypes.Source
	}{
		{
			name: "deps",
			sources: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/alpine@sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
					Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
				},
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
				{
					Type: "http",
					Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
					Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
				},
			},
			deps: map[string]binfotypes.BuildInfo{
				"base": {
					Frontend: "dockerfile.v0",
					Sources: []binfotypes.Source{
						{
							Type: "docker-image",
							Ref:  "docker.io/library/alpine:latest",
							Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
						},
					},
				},
			},
			want: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
				{
					Type: "http",
					Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
					Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
				},
			},
		},
		{
			name: "multideps",
			sources: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/alpine@sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
					Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
				},
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:1.35.0@sha256:20246233b52de844fa516f8c51234f1441e55e71ecdd1a1d91ebb252e1fd4603",
					Pin:  "sha256:20246233b52de844fa516f8c51234f1441e55e71ecdd1a1d91ebb252e1fd4603",
				},
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
				{
					Type: "http",
					Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
					Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
				},
			},
			deps: map[string]binfotypes.BuildInfo{
				"base": {
					Frontend: "dockerfile.v0",
					Sources: []binfotypes.Source{
						{
							Type: "docker-image",
							Ref:  "docker.io/library/alpine:latest",
							Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
						},
						{
							Type: "docker-image",
							Ref:  "docker.io/library/busybox:1.35.0",
							Pin:  "sha256:20246233b52de844fa516f8c51234f1441e55e71ecdd1a1d91ebb252e1fd4603",
						},
					},
				},
			},
			want: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
				{
					Type: "http",
					Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
					Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
				},
			},
		},
		{
			name: "regular",
			sources: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/alpine@sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
					Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
				},
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
			},
			deps: map[string]binfotypes.BuildInfo{
				"base": {
					Frontend: "dockerfile.v0",
					Sources: []binfotypes.Source{
						{
							Type: "docker-image",
							Ref:  "docker.io/library/alpine:latest",
							Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
						},
					},
				},
			},
			want: []binfotypes.Source{
				{
					Type: "docker-image",
					Ref:  "docker.io/library/busybox:latest",
					Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
				},
			},
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dedupSources(tt.sources, allDepsSources(tt.deps, nil)))
		})
	}
}

func TestFilterAttrs(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		attrs map[string]*string
		want  map[string]*string
	}{
		{
			name: "simple",
			key:  exptypes.ExporterBuildInfo,
			attrs: map[string]*string{
				"build-arg:foo": stringPtr("bar"),
				"cmdline":       stringPtr("crazymax/dockerfile:buildattrs"),
				"context":       stringPtr("https://github.com/crazy-max/buildkit-buildsources-test.git#master"),
				"filename":      stringPtr("Dockerfile"),
				"source":        stringPtr("crazymax/dockerfile:master"),
			},
			want: map[string]*string{
				"build-arg:foo": stringPtr("bar"),
				"context":       stringPtr("https://github.com/crazy-max/buildkit-buildsources-test.git#master"),
				"filename":      stringPtr("Dockerfile"),
				"source":        stringPtr("crazymax/dockerfile:master"),
			},
		},
		{
			name: "multiplatform",
			key:  exptypes.ExporterBuildInfo + "/linux/amd64",
			attrs: map[string]*string{
				"build-arg:bar":                    stringPtr("foo"),
				"build-arg:foo":                    stringPtr("bar"),
				"context:base::linux/amd64":        stringPtr("input:base::linux/amd64"),
				"context:base::linux/arm64":        stringPtr("input:base::linux/arm64"),
				"dockerfilekey":                    stringPtr("dockerfile2"),
				"input-metadata:base::linux/amd64": stringPtr("{\"containerimage.buildinfo\":\"eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJzb3VyY2VzIjpbeyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2xpYnJhcnkvYWxwaW5lOmxhdGVzdCIsInBpbiI6InNoYTI1NjplN2Q4OGRlNzNkYjNkM2ZkOWIyZDYzYWE3ZjQ0N2ExMGZkMDIyMGI3Y2JmMzk4MDNjODAzZjJhZjliYTI1NmIzIn1dfQ==\",\"containerimage.config\":\"eyJhcmNoaXRlY3R1cmUiOiJhbWQ2NCIsIm9zIjoibGludXgiLCJyb290ZnMiOnsidHlwZSI6ImxheWVycyIsImRpZmZfaWRzIjpbInNoYTI1Njo4ZDNhYzM0ODk5OTY0MjNmNTNkNjA4N2M4MTE4MDAwNjI2M2I3OWYyMDZkM2ZkZWM5ZTY2ZjBlMjdjZWI4NzU5Il19LCJoaXN0b3J5IjpbeyJjcmVhdGVkIjoiMjAyMS0xMS0yNFQyMDoxOTo0MC4xOTk3MDA5NDZaIiwiY3JlYXRlZF9ieSI6Ii9iaW4vc2ggLWMgIyhub3ApIEFERCBmaWxlOjkyMzNmNmYyMjM3ZDc5NjU5YTk1MjFmN2UzOTBkZjIxN2NlYzQ5ZjFhOGFhM2ExMjE0N2JiY2ExOTU2YWNkYjkgaW4gLyAifSx7ImNyZWF0ZWQiOiIyMDIxLTExLTI0VDIwOjE5OjQwLjQ4MzM2NzU0NloiLCJjcmVhdGVkX2J5IjoiL2Jpbi9zaCAtYyAjKG5vcCkgIENNRCBbXCIvYmluL3NoXCJdIiwiZW1wdHlfbGF5ZXIiOnRydWV9LHsiY3JlYXRlZF9ieSI6IkFSRyBUQVJHRVRBUkNIIiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAiLCJlbXB0eV9sYXllciI6dHJ1ZX0seyJjcmVhdGVkX2J5IjoiRU5WIEZPTz1iYXItYW1kNjQiLCJjb21tZW50IjoiYnVpbGRraXQuZG9ja2VyZmlsZS52MCIsImVtcHR5X2xheWVyIjp0cnVlfSx7ImNyZWF0ZWRfYnkiOiJSVU4gfDEgVEFSR0VUQVJDSD1hbWQ2NCAvYmluL3NoIC1jIGVjaG8gXCJmb28gJFRBUkdFVEFSQ0hcIiBcdTAwM2UgL291dCAjIGJ1aWxka2l0IiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAifV0sImNvbmZpZyI6eyJFbnYiOlsiUEFUSD0vdXNyL2xvY2FsL3NiaW46L3Vzci9sb2NhbC9iaW46L3Vzci9zYmluOi91c3IvYmluOi9zYmluOi9iaW4iLCJGT089YmFyLWFtZDY0Il0sIkNtZCI6WyIvYmluL3NoIl0sIk9uQnVpbGQiOm51bGx9fQ==\"}"),
				"input-metadata:base::linux/arm64": stringPtr("{\"containerimage.buildinfo\":\"eyJmcm9udGVuZCI6ImRvY2tlcmZpbGUudjAiLCJzb3VyY2VzIjpbeyJ0eXBlIjoiZG9ja2VyLWltYWdlIiwicmVmIjoiZG9ja2VyLmlvL2xpYnJhcnkvYWxwaW5lOmxhdGVzdCIsInBpbiI6InNoYTI1NjplN2Q4OGRlNzNkYjNkM2ZkOWIyZDYzYWE3ZjQ0N2ExMGZkMDIyMGI3Y2JmMzk4MDNjODAzZjJhZjliYTI1NmIzIn1dfQ==\",\"containerimage.config\":\"eyJhcmNoaXRlY3R1cmUiOiJhcm02NCIsIm9zIjoibGludXgiLCJyb290ZnMiOnsidHlwZSI6ImxheWVycyIsImRpZmZfaWRzIjpbInNoYTI1Njo4ZDNhYzM0ODk5OTY0MjNmNTNkNjA4N2M4MTE4MDAwNjI2M2I3OWYyMDZkM2ZkZWM5ZTY2ZjBlMjdjZWI4NzU5Il19LCJoaXN0b3J5IjpbeyJjcmVhdGVkIjoiMjAyMS0xMS0yNFQyMDoxOTo0MC4xOTk3MDA5NDZaIiwiY3JlYXRlZF9ieSI6Ii9iaW4vc2ggLWMgIyhub3ApIEFERCBmaWxlOjkyMzNmNmYyMjM3ZDc5NjU5YTk1MjFmN2UzOTBkZjIxN2NlYzQ5ZjFhOGFhM2ExMjE0N2JiY2ExOTU2YWNkYjkgaW4gLyAifSx7ImNyZWF0ZWQiOiIyMDIxLTExLTI0VDIwOjE5OjQwLjQ4MzM2NzU0NloiLCJjcmVhdGVkX2J5IjoiL2Jpbi9zaCAtYyAjKG5vcCkgIENNRCBbXCIvYmluL3NoXCJdIiwiZW1wdHlfbGF5ZXIiOnRydWV9LHsiY3JlYXRlZF9ieSI6IkFSRyBUQVJHRVRBUkNIIiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAiLCJlbXB0eV9sYXllciI6dHJ1ZX0seyJjcmVhdGVkX2J5IjoiRU5WIEZPTz1iYXItYXJtNjQiLCJjb21tZW50IjoiYnVpbGRraXQuZG9ja2VyZmlsZS52MCIsImVtcHR5X2xheWVyIjp0cnVlfSx7ImNyZWF0ZWRfYnkiOiJSVU4gfDEgVEFSR0VUQVJDSD1hcm02NCAvYmluL3NoIC1jIGVjaG8gXCJmb28gJFRBUkdFVEFSQ0hcIiBcdTAwM2UgL291dCAjIGJ1aWxka2l0IiwiY29tbWVudCI6ImJ1aWxka2l0LmRvY2tlcmZpbGUudjAifV0sImNvbmZpZyI6eyJFbnYiOlsiUEFUSD0vdXNyL2xvY2FsL3NiaW46L3Vzci9sb2NhbC9iaW46L3Vzci9zYmluOi91c3IvYmluOi9zYmluOi9iaW4iLCJGT089YmFyLWFybTY0Il0sIkNtZCI6WyIvYmluL3NoIl0sIk9uQnVpbGQiOm51bGx9fQ==\"}"),
				"platform":                         stringPtr("linux/amd64,linux/arm64"),
			},
			want: map[string]*string{
				"build-arg:bar": stringPtr("foo"),
				"build-arg:foo": stringPtr("bar"),
				"context:base":  stringPtr("input:base"),
			},
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, filterAttrs(tt.key, tt.attrs))
		})
	}
}

func TestFormat(t *testing.T) {
	bi := binfotypes.BuildInfo{
		Frontend: "dockerfile.v0",
		Attrs: map[string]*string{
			"build-arg:foo": stringPtr("bar"),
			"context:base":  stringPtr("input:base"),
			"filename":      stringPtr("Dockerfile"),
			"source":        stringPtr("crazymax/dockerfile:master"),
		},
		Sources: []binfotypes.Source{
			{
				Type: "docker-image",
				Ref:  "docker.io/library/busybox:latest",
				Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
			},
			{
				Type: "http",
				Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
				Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
			},
		},
		Deps: map[string]binfotypes.BuildInfo{
			"base": {
				Frontend: "dockerfile.v0",
				Attrs: map[string]*string{
					"build-arg:foo": stringPtr("bar"),
					"filename":      stringPtr("Dockerfile2"),
					"source":        stringPtr("crazymax/dockerfile:master"),
				},
				Sources: []binfotypes.Source{
					{
						Type: "docker-image",
						Ref:  "docker.io/library/alpine:latest",
						Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
					},
					{
						Type: "docker-image",
						Ref:  "docker.io/library/busybox:1.35.0",
						Pin:  "sha256:20246233b52de844fa516f8c51234f1441e55e71ecdd1a1d91ebb252e1fd4603",
					},
				},
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
						Type: "docker-image",
						Ref:  "docker.io/library/alpine:latest",
						Pin:  "sha256:e7d88de73db3d3fd9b2d63aa7f447a10fd0220b7cbf39803c803f2af9ba256b3",
					},
					{
						Type: "docker-image",
						Ref:  "docker.io/library/busybox:1.35.0",
						Pin:  "sha256:20246233b52de844fa516f8c51234f1441e55e71ecdd1a1d91ebb252e1fd4603",
					},
					{
						Type: "docker-image",
						Ref:  "docker.io/library/busybox:latest",
						Pin:  "sha256:b69959407d21e8a062e0416bf13405bb2b71ed7a84dde4158ebafacfa06f5578",
					},
					{
						Type: "http",
						Ref:  "https://raw.githubusercontent.com/moby/moby/master/README.md",
						Pin:  "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c",
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

func TestReduceMapString(t *testing.T) {
	cases := []struct {
		name     string
		m1       map[string]*string
		m2       map[string]string
		expected map[string]string
	}{
		{
			name:     "first",
			m1:       map[string]*string{"foo": stringPtr("bar"), "abc": stringPtr("def")},
			m2:       map[string]string{"bar": "foo", "abc": "ghi"},
			expected: map[string]string{"foo": "bar", "abc": "def", "bar": "foo"},
		},
		{
			name:     "last",
			m1:       map[string]*string{"bar": stringPtr("foo"), "abc": stringPtr("ghi")},
			m2:       map[string]string{"foo": "bar", "abc": "def"},
			expected: map[string]string{"bar": "foo", "abc": "ghi", "foo": "bar"},
		},
		{
			name:     "nilattr",
			m1:       map[string]*string{"foo": stringPtr("bar"), "abc": stringPtr("fgh"), "baz": nil},
			m2:       map[string]string{"foo": "bar", "baz": "fuu"},
			expected: map[string]string{"foo": "bar", "abc": "fgh", "baz": "fuu"},
		},
		{
			name:     "null1",
			m1:       nil,
			m2:       map[string]string{"foo": "bar", "abc": "def"},
			expected: map[string]string{"foo": "bar", "abc": "def"},
		},
		{
			name:     "null2",
			m1:       map[string]*string{"foo": stringPtr("bar"), "abc": stringPtr("def")},
			m2:       nil,
			expected: map[string]string{"foo": "bar", "abc": "def"},
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, reduceMapString(tt.m2, tt.m1))
		})
	}
}

// stringPtr returns a pointer to the string value passed in.
func stringPtr(v string) *string {
	return &v
}

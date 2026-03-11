package provenance

import (
	"testing"

	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	digest "github.com/opencontainers/go-digest"
	packageurl "github.com/package-url/packageurl-go"
	"github.com/stretchr/testify/require"
)

func TestSLSAMaterialsImageBlobPURL(t *testing.T) {
	t.Parallel()

	dgst := digest.FromString("blobdata")
	ms, err := slsaMaterials(provenancetypes.Sources{
		ImageBlobs: []provenancetypes.ImageBlobSource{
			{
				Ref:    "example.com/ns/repo@" + dgst.String(),
				Digest: dgst,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, ms, 1)

	p, err := packageurl.FromString(ms[0].URI)
	require.NoError(t, err)
	require.Equal(t, packageurl.TypeDocker, p.Type)
	require.Equal(t, "example.com/ns", p.Namespace)
	require.Equal(t, "repo", p.Name)
	require.Equal(t, "", p.Version)

	q := p.Qualifiers.Map()
	require.Equal(t, "blob", q["ref_type"])
	require.Equal(t, dgst.String(), q["digest"])

	require.Equal(t, dgst.Hex(), ms[0].Digest[dgst.Algorithm().String()])
}

func TestSLSAMaterialsOCILayoutBlobPURL(t *testing.T) {
	t.Parallel()

	dgst := digest.FromString("blobdata")
	ms, err := slsaMaterials(provenancetypes.Sources{
		ImageBlobs: []provenancetypes.ImageBlobSource{
			{
				Ref:    "example.com/ns/repo@" + dgst.String(),
				Digest: dgst,
				Local:  true,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, ms, 1)

	p, err := packageurl.FromString(ms[0].URI)
	require.NoError(t, err)
	require.Equal(t, packageurl.TypeOCI, p.Type)

	q := p.Qualifiers.Map()
	require.Equal(t, "blob", q["ref_type"])
	require.Equal(t, dgst.String(), q["digest"])
}

func TestFilterArgs(t *testing.T) {
	t.Parallel()

	args := map[string]string{
		"build-arg:FOO":      "bar",
		"label:maintainer":   "me",
		"platform":           "linux/amd64",
		"cgroup-parent":      "test",
		"image-resolve-mode": "pull",
		"cache-imports":      "type=local,src=/tmp",
		"attest:provenance":  "mode=max",
		"filename":           "Dockerfile",
		"target":             "build",
	}

	out := FilterArgs(args)

	// Host-specific and attest: args should be filtered
	require.NotContains(t, out, "platform")
	require.NotContains(t, out, "cgroup-parent")
	require.NotContains(t, out, "image-resolve-mode")
	require.NotContains(t, out, "cache-imports")
	require.NotContains(t, out, "attest:provenance")

	// Regular args should be kept
	require.Equal(t, "bar", out["build-arg:FOO"])
	require.Equal(t, "me", out["label:maintainer"])
	require.Equal(t, "Dockerfile", out["filename"])
	require.Equal(t, "build", out["target"])
}

func TestFilterArgsRedactsContext(t *testing.T) {
	t.Parallel()

	args := map[string]string{
		"context": "https://user:pass@github.com/moby/buildkit.git",
	}

	out := FilterArgs(args)
	require.NotContains(t, out["context"], "pass")
}

func TestDigestSetForCommit(t *testing.T) {
	t.Parallel()

	// SHA-1 (40 hex chars)
	ds := digestSetForCommit("abc123def456abc123def456abc123def456abcd")
	require.Equal(t, "abc123def456abc123def456abc123def456abcd", ds["sha1"])
	require.Empty(t, ds["sha256"])

	// SHA-256 (64 hex chars)
	sha256commit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.Len(t, sha256commit, 64)
	ds = digestSetForCommit(sha256commit)
	require.Equal(t, sha256commit, ds["sha256"])
	require.Empty(t, ds["sha1"])
}

func TestFindMaterial(t *testing.T) {
	t.Parallel()

	srcs := provenancetypes.Sources{
		Git: []provenancetypes.GitSource{
			{URL: "https://github.com/moby/buildkit.git", Commit: "abc123def456abc123def456abc123def456abcd"},
		},
		HTTP: []provenancetypes.HTTPSource{
			{URL: "https://example.com/file.tar.gz", Digest: digest.FromString("data")},
		},
	}

	m, ok := findMaterial(srcs, "https://github.com/moby/buildkit.git")
	require.True(t, ok)
	require.Equal(t, "https://github.com/moby/buildkit.git", m.URI)

	m, ok = findMaterial(srcs, "https://example.com/file.tar.gz")
	require.True(t, ok)
	require.Equal(t, "https://example.com/file.tar.gz", m.URI)

	_, ok = findMaterial(srcs, "https://notfound.com")
	require.False(t, ok)
}

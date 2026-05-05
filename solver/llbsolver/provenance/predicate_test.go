package provenance

import (
	"strings"
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

func TestSLSAMaterialsGitBundlePURL(t *testing.T) {
	t.Parallel()

	commitHex := "abc123def456abc123def456abc123def456abcd"
	bundleDgst := digest.FromString("bundle")
	gitURL := "https://github.com/moby/buildkit.git#master"

	t.Run("registry", func(t *testing.T) {
		ms, err := slsaMaterials(provenancetypes.Sources{
			Git: []provenancetypes.GitSource{
				{
					URL:    gitURL,
					Commit: commitHex,
					Bundle: &provenancetypes.GitBundle{
						URL: "docker-image+blob://example.com/ns/repo@" + bundleDgst.String(),
					},
				},
			},
		})
		require.NoError(t, err)
		// one for the git source itself, one for the bundle material
		require.Len(t, ms, 2)

		// Git material is the raw repository URL (same shape as a
		// non-bundle git source).
		gitMaterial := ms[0]
		require.Equal(t, gitURL, gitMaterial.URI)
		require.Equal(t, commitHex, gitMaterial.Digest["sha1"])

		// Bundle material is a purl with a vcs_url qualifier pointing
		// back at the git URL.
		bundleMaterial := ms[1]
		require.True(t, strings.HasPrefix(bundleMaterial.URI, "pkg:docker/"),
			"expected docker purl, got %q", bundleMaterial.URI)
		bp, err := packageurl.FromString(bundleMaterial.URI)
		require.NoError(t, err)
		require.Equal(t, packageurl.TypeDocker, bp.Type)
		bq := bp.Qualifiers.Map()
		require.Equal(t, "bundle", bq["ref_type"])
		require.Equal(t, gitURL, bq["vcs_url"])
		require.Equal(t, bundleDgst.Hex(), bundleMaterial.Digest[bundleDgst.Algorithm().String()])
	})

	t.Run("oci-layout", func(t *testing.T) {
		ms, err := slsaMaterials(provenancetypes.Sources{
			Git: []provenancetypes.GitSource{
				{
					URL:    gitURL,
					Commit: commitHex,
					Bundle: &provenancetypes.GitBundle{
						URL: "oci-layout+blob://example.com/ns/repo@" + bundleDgst.String(),
					},
				},
			},
		})
		require.NoError(t, err)
		require.Len(t, ms, 2)

		// Git material is the raw repository URL.
		gitMaterial := ms[0]
		require.Equal(t, gitURL, gitMaterial.URI)
		require.Equal(t, commitHex, gitMaterial.Digest["sha1"])

		// Bundle material is a pkg:oci purl for an OCI-layout bundle.
		bundleMaterial := ms[1]
		require.True(t, strings.HasPrefix(bundleMaterial.URI, "pkg:oci/"),
			"expected oci purl, got %q", bundleMaterial.URI)
		bp, err := packageurl.FromString(bundleMaterial.URI)
		require.NoError(t, err)
		require.Equal(t, packageurl.TypeOCI, bp.Type)
		bq := bp.Qualifiers.Map()
		require.Equal(t, "bundle", bq["ref_type"])
		require.Equal(t, gitURL, bq["vcs_url"])
	})

	t.Run("non-bundle git source unchanged", func(t *testing.T) {
		// A plain git source (no Bundle) should still emit the raw URL
		// verbatim, not a purl. This guards the non-bundle contract.
		ms, err := slsaMaterials(provenancetypes.Sources{
			Git: []provenancetypes.GitSource{
				{
					URL:    gitURL,
					Commit: commitHex,
				},
			},
		})
		require.NoError(t, err)
		require.Len(t, ms, 1)
		require.Equal(t, gitURL, ms[0].URI)
		require.Equal(t, commitHex, ms[0].Digest["sha1"])
	})
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
			{URL: "https://github.com/moby/buildkit.git#refs/heads/master", Commit: "def123def456abc123def456abc123def456abcd"},
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

	m, ok = findMaterial(srcs, "https://github.com/moby/buildkit.git?branch=master&subdir=src")
	require.True(t, ok)
	require.Equal(t, "https://github.com/moby/buildkit.git#refs/heads/master:src", m.URI)
	require.Equal(t, "def123def456abc123def456abc123def456abcd", m.Digest["sha1"])

	_, ok = findMaterial(srcs, "https://notfound.com")
	require.False(t, ok)
}

func TestNewPredicatePreservesRequestSecretsSSHAndInputs(t *testing.T) {
	t.Parallel()

	c := &Capture{
		Request: provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args: map[string]string{
				"context":  "https://example.invalid/context.git",
				"filename": "Dockerfile",
			},
			Secrets: []*provenancetypes.Secret{
				{ID: "mysecret", Optional: true},
			},
			SSH: []*provenancetypes.SSH{
				{ID: "default", Optional: true},
			},
			Inputs: map[string]*provenancetypes.RequestProvenance{
				"base": {
					Request: &provenancetypes.Parameters{
						Frontend: "dockerfile.v0",
						Args:     map[string]string{"target": "base"},
					},
				},
			},
		},
	}

	pr, err := NewPredicate(c)
	require.NoError(t, err)

	req := pr.BuildDefinition.ExternalParameters.Request
	require.Equal(t, "dockerfile.v0", req.Frontend)
	require.Nil(t, req.Args)
	require.Len(t, req.Secrets, 1)
	require.Equal(t, "mysecret", req.Secrets[0].ID)
	require.True(t, req.Secrets[0].Optional)
	require.Len(t, req.SSH, 1)
	require.Equal(t, "default", req.SSH[0].ID)
	require.True(t, req.SSH[0].Optional)
	require.Contains(t, req.Inputs, "base")
	require.Equal(t, "base", req.Inputs["base"].Request.Args["target"])
}

func TestNewPredicateGitConfigSourceSubdir(t *testing.T) {
	t.Parallel()

	commit := "abc123def456abc123def456abc123def456abcd"
	c := &Capture{
		Request: provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args: map[string]string{
				"context":  "https://github.com/moby/buildkit.git?branch=master&subdir=src",
				"filename": "Dockerfile",
			},
		},
		Sources: provenancetypes.Sources{
			Git: []provenancetypes.GitSource{
				{URL: "https://github.com/moby/buildkit.git#refs/heads/master", Commit: commit},
			},
		},
	}

	pr, err := NewPredicate(c)
	require.NoError(t, err)

	require.Equal(t, "https://github.com/moby/buildkit.git#refs/heads/master:src", pr.BuildDefinition.ExternalParameters.ConfigSource.URI)
	require.Equal(t, commit, pr.BuildDefinition.ExternalParameters.ConfigSource.Digest["sha1"])
	require.Equal(t, "Dockerfile", pr.BuildDefinition.ExternalParameters.ConfigSource.Path)
	require.Equal(t, "https://github.com/moby/buildkit.git#refs/heads/master", pr.BuildDefinition.ResolvedDependencies[0].URI)
	require.Equal(t, commit, pr.BuildDefinition.ResolvedDependencies[0].Digest["sha1"])
}

func TestNewPredicateKeepsContextSubdir(t *testing.T) {
	t.Parallel()

	c := &Capture{
		Request: provenancetypes.Parameters{
			Frontend: "dockerfile.v0",
			Args: map[string]string{
				"contextsubdir": "src",
				"filename":      "Dockerfile",
			},
		},
	}

	pr, err := NewPredicate(c)
	require.NoError(t, err)

	require.Equal(t, "", pr.BuildDefinition.ExternalParameters.ConfigSource.URI)
	require.Equal(t, "src", pr.BuildDefinition.ExternalParameters.Request.Args["contextsubdir"])
}

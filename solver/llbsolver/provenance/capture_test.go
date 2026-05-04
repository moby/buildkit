package provenance

import (
	"testing"

	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestCaptureAddImageDedup(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	img := provenancetypes.ImageSource{Ref: "docker.io/library/alpine:latest"}
	c.AddImage(img)
	c.AddImage(img) // duplicate

	require.Len(t, c.Sources.Images, 1)
}

func TestCaptureAddImageDifferentPlatforms(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	c.AddImage(provenancetypes.ImageSource{
		Ref:      "docker.io/library/alpine:latest",
		Platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"},
	})
	c.AddImage(provenancetypes.ImageSource{
		Ref:      "docker.io/library/alpine:latest",
		Platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"},
	})

	require.Len(t, c.Sources.Images, 2)
}

func TestCaptureAddImageSamePlatformDedup(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	p := &ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	c.AddImage(provenancetypes.ImageSource{
		Ref:      "docker.io/library/alpine:latest",
		Platform: p,
	})
	c.AddImage(provenancetypes.ImageSource{
		Ref:      "docker.io/library/alpine:latest",
		Platform: p,
	})

	require.Len(t, c.Sources.Images, 1)
}

func TestCaptureAddSecretMerge(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	c.AddSecret(provenancetypes.Secret{ID: "mysecret", Optional: true})
	c.AddSecret(provenancetypes.Secret{ID: "mysecret", Optional: false})

	require.Len(t, c.Secrets, 1)
	require.False(t, c.Secrets[0].Optional, "non-optional should win")
}

func TestCaptureAddSSHDefault(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	c.AddSSH(provenancetypes.SSH{ID: ""})
	require.Len(t, c.SSH, 1)
	require.Equal(t, "default", c.SSH[0].ID)
}

func TestCaptureAddGitRedactsCredentials(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	c.AddGit(provenancetypes.GitSource{
		URL:    "https://user:pass@github.com/moby/buildkit.git",
		Commit: "abc123",
	})
	require.Len(t, c.Sources.Git, 1)
	require.NotContains(t, c.Sources.Git[0].URL, "pass")
}

func TestCaptureAddGitDedup(t *testing.T) {
	t.Parallel()
	c := &Capture{}

	c.AddGit(provenancetypes.GitSource{URL: "https://github.com/moby/buildkit.git", Commit: "abc"})
	c.AddGit(provenancetypes.GitSource{URL: "https://github.com/moby/buildkit.git", Commit: "abc"})

	require.Len(t, c.Sources.Git, 1)
}

// TestCaptureAddGitBundleDedup pins the dedupe-key behavior: the same URL
// referenced once without and once with a bundle locator must record two
// separate materials, because the bundle carries its own identity. Identical
// bundle locators still dedupe.
func TestCaptureAddGitBundleDedup(t *testing.T) {
	t.Parallel()

	url := "https://github.com/moby/buildkit.git#master"
	bundleDgst := digest.FromString("bundle-content")
	bundleURL := "docker-image+blob://example.com/ns/repo@" + bundleDgst.String()

	t.Run("same URL, different bundle fields are preserved", func(t *testing.T) {
		c := &Capture{}
		c.AddGit(provenancetypes.GitSource{
			URL:    url,
			Commit: "abc",
		})
		c.AddGit(provenancetypes.GitSource{
			URL:    url,
			Commit: "abc",
			Bundle: &provenancetypes.GitBundle{URL: bundleURL},
		})
		require.Len(t, c.Sources.Git, 2)
		require.Nil(t, c.Sources.Git[0].Bundle)
		require.NotNil(t, c.Sources.Git[1].Bundle)
		require.Equal(t, bundleURL, c.Sources.Git[1].Bundle.URL)
	})

	t.Run("identical bundle tuple still dedupes", func(t *testing.T) {
		c := &Capture{}
		mk := func() provenancetypes.GitSource {
			return provenancetypes.GitSource{
				URL:    url,
				Commit: "abc",
				Bundle: &provenancetypes.GitBundle{URL: bundleURL},
			}
		}
		c.AddGit(mk())
		c.AddGit(mk())
		require.Len(t, c.Sources.Git, 1)
	})

	t.Run("different bundle URLs are preserved", func(t *testing.T) {
		c := &Capture{}
		dgst1 := digest.FromString("bundle-1")
		dgst2 := digest.FromString("bundle-2")
		c.AddGit(provenancetypes.GitSource{
			URL: url,
			Bundle: &provenancetypes.GitBundle{
				URL: "docker-image+blob://example.com/ns/repo@" + dgst1.String(),
			},
		})
		c.AddGit(provenancetypes.GitSource{
			URL: url,
			Bundle: &provenancetypes.GitBundle{
				URL: "docker-image+blob://example.com/ns/repo@" + dgst2.String(),
			},
		})
		require.Len(t, c.Sources.Git, 2)
	})
}

func TestCaptureMerge(t *testing.T) {
	t.Parallel()

	c1 := &Capture{}
	c1.AddImage(provenancetypes.ImageSource{Ref: "alpine"})
	c1.AddSecret(provenancetypes.Secret{ID: "s1"})

	c2 := &Capture{
		NetworkAccess:       true,
		IncompleteMaterials: true,
	}
	c2.AddImage(provenancetypes.ImageSource{Ref: "busybox"})
	c2.AddGit(provenancetypes.GitSource{URL: "https://example.com", Commit: "abc"})

	err := c1.Merge(c2)
	require.NoError(t, err)

	require.Len(t, c1.Sources.Images, 2)
	require.Len(t, c1.Sources.Git, 1)
	require.Len(t, c1.Secrets, 1)
	require.True(t, c1.NetworkAccess)
	require.True(t, c1.IncompleteMaterials)
}

func TestCaptureMergeNil(t *testing.T) {
	t.Parallel()
	c := &Capture{}
	require.NoError(t, c.Merge(nil))
}

func TestCaptureSort(t *testing.T) {
	t.Parallel()
	c := &Capture{}
	c.AddImage(provenancetypes.ImageSource{Ref: "zebra"})
	c.AddImage(provenancetypes.ImageSource{Ref: "alpha"})
	c.AddSecret(provenancetypes.Secret{ID: "z"})
	c.AddSecret(provenancetypes.Secret{ID: "a"})

	c.Sort()

	require.Equal(t, "alpha", c.Sources.Images[0].Ref)
	require.Equal(t, "zebra", c.Sources.Images[1].Ref)
	require.Equal(t, "a", c.Secrets[0].ID)
	require.Equal(t, "z", c.Secrets[1].ID)
}

func TestCaptureOptimizeImageSources(t *testing.T) {
	t.Parallel()
	dgst := digest.FromString("test")
	c := &Capture{}

	// Add tagged reference
	c.AddImage(provenancetypes.ImageSource{
		Ref: "docker.io/library/alpine:latest",
	})
	// Add digest reference for same image (defaults to name:latest)
	c.AddImage(provenancetypes.ImageSource{
		Ref: "docker.io/library/alpine@" + dgst.String(),
	})

	require.Len(t, c.Sources.Images, 2)

	err := c.OptimizeImageSources()
	require.NoError(t, err)

	// Digest ref should be filtered out since tag ref exists
	require.Len(t, c.Sources.Images, 1)
	require.Equal(t, "docker.io/library/alpine:latest", c.Sources.Images[0].Ref)
}

func TestCaptureAddSamples(t *testing.T) {
	t.Parallel()
	c := &Capture{}
	require.Nil(t, c.Samples)

	dgst := digest.FromString("op1")
	c.AddSamples(dgst, nil)

	require.NotNil(t, c.Samples)
	require.Contains(t, c.Samples, dgst)
}

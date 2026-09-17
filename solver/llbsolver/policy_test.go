package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/stretchr/testify/require"
)

func TestGitBundleSourceOp(t *testing.T) {
	const (
		bundle = "oci-layout+blob://local/git-bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		store  = "bundle-store"
		sid    = "bundle-session"
	)
	op := &pb.Op{
		Op: &pb.Op_Source{Source: &pb.SourceOp{
			Identifier: "git://example.com/repo.git",
			Attrs: map[string]string{
				pb.AttrGitBundle:          bundle,
				pb.AttrGitChecksum:        "1111111111111111111111111111111111111111",
				pb.AttrOCILayoutSessionID: sid,
				pb.AttrOCILayoutStoreID:   store,
			},
		}},
		Platform: &pb.Platform{OS: "linux", Architecture: "amd64"},
	}

	bundleOp, ok, err := gitBundleSourceOp(op)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, bundle, bundleOp.GetSource().Identifier)
	require.Equal(t, map[string]string{
		pb.AttrOCILayoutSessionID: sid,
		pb.AttrOCILayoutStoreID:   store,
	}, bundleOp.GetSource().Attrs)
	require.Equal(t, op.Platform, bundleOp.Platform)

	delete(op.GetSource().Attrs, pb.AttrOCILayoutStoreID)
	bundleOp, ok, err = gitBundleSourceOp(op)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "local/git-bundle", bundleOp.GetSource().Attrs[pb.AttrOCILayoutStoreID])
}

func TestApplyGitBundleSourceOp(t *testing.T) {
	gitSource := &pb.SourceOp{Attrs: map[string]string{
		pb.AttrGitBundle:          "docker-image+blob://old.example.com/bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		pb.AttrOCILayoutSessionID: "old-session",
		pb.AttrOCILayoutStoreID:   "old-store",
	}}
	bundleSource := &pb.SourceOp{
		Identifier: "oci-layout+blob://converted@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Attrs: map[string]string{
			pb.AttrOCILayoutSessionID: "new-session",
			pb.AttrOCILayoutStoreID:   "new-store",
		},
	}

	applyGitBundleSourceOp(gitSource, bundleSource)
	require.Equal(t, bundleSource.Identifier, gitSource.Attrs[pb.AttrGitBundle])
	require.Equal(t, "new-session", gitSource.Attrs[pb.AttrOCILayoutSessionID])
	require.Equal(t, "new-store", gitSource.Attrs[pb.AttrOCILayoutStoreID])

	bundleSource.Attrs = nil
	applyGitBundleSourceOp(gitSource, bundleSource)
	require.NotContains(t, gitSource.Attrs, pb.AttrOCILayoutSessionID)
	require.NotContains(t, gitSource.Attrs, pb.AttrOCILayoutStoreID)
}

func TestApplyConvertedGitBundleWithImplicitStore(t *testing.T) {
	const (
		original  = "oci-layout+blob://local/original@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		converted = "oci-layout+blob://local/converted@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	gitOp := &pb.Op{Op: &pb.Op_Source{Source: &pb.SourceOp{
		Identifier: "git://example.com/repo.git",
		Attrs:      map[string]string{pb.AttrGitBundle: original},
	}}}
	bundleOp, ok, err := gitBundleSourceOp(gitOp)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "local/original", bundleOp.GetSource().Attrs[pb.AttrOCILayoutStoreID])

	mutated, err := sourcepolicy.NewEngine([]*spb.Policy{{Rules: []*spb.Rule{
		{
			Action:   spb.PolicyAction_CONVERT,
			Selector: &spb.Selector{Identifier: original, MatchType: spb.MatchType_EXACT},
			Updates:  &spb.Update{Identifier: converted},
		},
	}}}).Evaluate(t.Context(), bundleOp.GetSource())
	require.NoError(t, err)
	require.True(t, mutated)

	applyGitBundleSourceOp(gitOp.GetSource(), bundleOp.GetSource())
	require.Equal(t, converted, gitOp.GetSource().Attrs[pb.AttrGitBundle])
	require.NotContains(t, gitOp.GetSource().Attrs, pb.AttrOCILayoutStoreID)

	convertedOp, ok, err := gitBundleSourceOp(gitOp)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "local/converted", convertedOp.GetSource().Attrs[pb.AttrOCILayoutStoreID])
}

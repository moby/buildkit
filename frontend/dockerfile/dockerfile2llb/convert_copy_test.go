package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/stretchr/testify/require"
)

func sourceIdentifiers(t *testing.T, df string) []string {
	t.Helper()
	res, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)
	def, err := res.State.Marshal(appcontext.Context())
	require.NoError(t, err)
	var ids []string
	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.Unmarshal(dt))
		if src := op.GetSource(); src != nil {
			ids = append(ids, src.Identifier)
		}
	}
	return ids
}

func TestAddKeepGitDirNonSuffixedURL(t *testing.T) {
	t.Parallel()
	// with --keep-git-dir, a non-".git" URL must become a git source
	ids := sourceIdentifiers(t, `
FROM scratch
ADD --keep-git-dir=true https://git.sr.ht/~foo/bar#main /dst
`)
	require.Contains(t, ids, "git://git.sr.ht/~foo/bar#main")
	require.Len(t, ids, 1)

	// same with --keep-git-dir=false
	ids = sourceIdentifiers(t, `
FROM scratch
ADD --keep-git-dir=false https://git.sr.ht/~foo/bar#main /dst
`)
	require.Contains(t, ids, "git://git.sr.ht/~foo/bar#main")

	// without the flag, a non-".git" URL stays an HTTP source
	ids = sourceIdentifiers(t, `
FROM scratch
ADD https://git.sr.ht/~foo/bar /dst
`)
	require.Contains(t, ids, "https://git.sr.ht/~foo/bar")

	// without the flag, a ".git" URL is still a git source
	ids = sourceIdentifiers(t, `
FROM scratch
ADD https://github.com/moby/buildkit.git#master /dst
`)
	require.Contains(t, ids, "git://github.com/moby/buildkit.git#master")
}

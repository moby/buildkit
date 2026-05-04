package dockerui

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func findLocalAttr(t *testing.T, def *llb.Definition, attr string) string {
	t.Helper()
	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.Unmarshal(dt))
		src := op.GetSource()
		if src == nil {
			continue
		}
		if v, ok := src.Attrs[attr]; ok {
			return v
		}
	}
	return ""
}

func TestMainContextSharedSessionDropsFollowPaths(t *testing.T) {
	ctx := context.Background()
	callerOpts := []llb.LocalOption{
		llb.FollowPaths([]string{"app/cfg"}),
	}

	t.Run("non-shared session preserves caller FollowPaths", func(t *testing.T) {
		opts := mainContextLocalOpts("context", "sess-A", nil, callerOpts, false)
		st := llb.Local("context", opts...)
		def, err := st.Marshal(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, findLocalAttr(t, def, pb.AttrFollowPaths))
	})

	t.Run("shared session clears caller FollowPaths", func(t *testing.T) {
		opts := mainContextLocalOpts("context", "sess-shared", nil, callerOpts, true)
		st := llb.Local("context", opts...)
		def, err := st.Marshal(ctx)
		require.NoError(t, err)
		require.Empty(t, findLocalAttr(t, def, pb.AttrFollowPaths))
	})

	t.Run("shared session makes divergent caller FollowPaths identical", func(t *testing.T) {
		optsA := mainContextLocalOpts("context", "sess-shared", nil,
			[]llb.LocalOption{llb.FollowPaths([]string{"app/cfg"})}, true)
		optsB := mainContextLocalOpts("context", "sess-shared", nil,
			[]llb.LocalOption{llb.FollowPaths([]string{"tools/cfg"})}, true)

		stA := llb.Local("context", optsA...)
		stB := llb.Local("context", optsB...)
		defA, err := stA.Marshal(ctx)
		require.NoError(t, err)
		defB, err := stB.Marshal(ctx)
		require.NoError(t, err)

		require.Equal(t, defA.Def, defB.Def)
	})
}

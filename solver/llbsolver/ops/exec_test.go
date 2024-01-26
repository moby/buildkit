package ops

import (
	"context"
	"testing"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestDedupePaths(t *testing.T) {
	res := dedupePaths([]string{"/Gemfile", "/Gemfile/foo"})
	require.Equal(t, []string{"/Gemfile"}, res)

	res = dedupePaths([]string{"/Gemfile/bar", "/Gemfile/foo"})
	require.Equal(t, []string{"/Gemfile/bar", "/Gemfile/foo"}, res)

	res = dedupePaths([]string{"/Gemfile", "/Gemfile.lock"})
	require.Equal(t, []string{"/Gemfile", "/Gemfile.lock"}, res)

	res = dedupePaths([]string{"/Gemfile.lock", "/Gemfile"})
	require.Equal(t, []string{"/Gemfile", "/Gemfile.lock"}, res)

	res = dedupePaths([]string{"/foo", "/Gemfile", "/Gemfile/foo"})
	require.Equal(t, []string{"/Gemfile", "/foo"}, res)

	res = dedupePaths([]string{"/foo/bar/baz", "/foo/bara", "/foo/bar/bax", "/foo/bar"})
	require.Equal(t, []string{"/foo/bar", "/foo/bara"}, res)

	res = dedupePaths([]string{"/", "/foo"})
	require.Equal(t, []string{"/"}, res)
}

func TestExecOpCacheMap(t *testing.T) {
	type testCase struct {
		name     string
		op1, op2 *ExecOp
		xMatch   bool
	}

	newExecOp := func(opts ...func(*ExecOp)) *ExecOp {
		op := &ExecOp{op: &pb.ExecOp{Meta: &pb.Meta{}}}
		for _, opt := range opts {
			opt(op)
		}
		return op
	}

	withNewMount := func(p string, cache *pb.CacheOpt) func(*ExecOp) {
		return func(op *ExecOp) {
			m := &pb.Mount{
				Dest:  p,
				Input: pb.InputIndex(op.numInputs),
				// Generate a new selector for each mount since this should not effect the cache key.
				// This helps exercise that code path.
				Selector: identity.NewID(),
			}
			if cache != nil {
				m.CacheOpt = cache
				m.MountType = pb.MountType_CACHE
			}
			op.op.Mounts = append(op.op.Mounts, m)
			op.numInputs++
		}
	}

	withEmptyMounts := func(op *ExecOp) {
		op.op.Mounts = []*pb.Mount{}
	}

	testCases := []testCase{
		{name: "empty", op1: newExecOp(), op2: newExecOp(), xMatch: true},
		{
			name:   "empty vs with non-nil but empty mounts should match",
			op1:    newExecOp(),
			op2:    newExecOp(withEmptyMounts),
			xMatch: true,
		},
		{
			name:   "both non-nil but empty mounts should match",
			op1:    newExecOp(withEmptyMounts),
			op2:    newExecOp(withEmptyMounts),
			xMatch: true,
		},
		{
			name:   "non-nil but empty mounts vs with mounts should not match",
			op1:    newExecOp(withEmptyMounts),
			op2:    newExecOp(withNewMount("/foo", nil)),
			xMatch: false,
		},
		{
			name:   "mounts to different paths should not match",
			op1:    newExecOp(withNewMount("/foo", nil)),
			op2:    newExecOp(withNewMount("/bar", nil)),
			xMatch: false,
		},
		{
			name:   "mounts to same path should match",
			op1:    newExecOp(withNewMount("/foo", nil)),
			op2:    newExecOp(withNewMount("/foo", nil)),
			xMatch: true,
		},
		{
			name:   "cache mount should not match non-cache mount at same path",
			op1:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someID"})),
			op2:    newExecOp(withNewMount("/foo", nil)),
			xMatch: false,
		},
		{
			name:   "different cache id's at the same path should match",
			op1:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someID"})),
			op2:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someOtherID"})),
			xMatch: true,
		},
		{
			// This is a special case for default dockerfile cache mounts for backwards compatibility.
			name:   "default dockerfile cache mount should not match the same cache mount but with different sharing",
			op1:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "/foo"})),
			op2:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "/foo", Sharing: pb.CacheSharingOpt_LOCKED})),
			xMatch: false,
		},
		{
			name:   "cache mounts with the same ID but different sharing options should match",
			op1:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someID", Sharing: 0})),
			op2:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someID", Sharing: 1})),
			xMatch: true,
		},
		{
			name:   "cache mounts with different IDs and different sharing should match at the same path",
			op1:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someID", Sharing: 0})),
			op2:    newExecOp(withNewMount("/foo", &pb.CacheOpt{ID: "someOtherID", Sharing: 1})),
			xMatch: true,
		},
	}

	ctx := context.Background()
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m1, ok, err := tc.op1.CacheMap(ctx, session.NewGroup(t.Name()), 1)
			require.NoError(t, err)
			require.True(t, ok)

			m2, ok, err := tc.op2.CacheMap(ctx, session.NewGroup(t.Name()), 1)
			require.NoError(t, err)
			require.True(t, ok)

			if tc.xMatch {
				require.Equal(t, m1.Digest, m2.Digest, "\n\nm1: %+v\nm2: %+v", m1, m2)
			} else {
				require.NotEqual(t, m1.Digest, m2.Digest, "\n\nm1: %+v\nm2: %+v", m1, m2)
			}
		})
	}
}

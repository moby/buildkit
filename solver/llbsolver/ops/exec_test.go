package ops

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDedupPaths(t *testing.T) {
	res := dedupePaths([]string{"Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile"}, res)

	res = dedupePaths([]string{"Gemfile/bar", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile/bar", "Gemfile/foo"}, res)

	res = dedupePaths([]string{"Gemfile", "Gemfile.lock"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"Gemfile.lock", "Gemfile"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"foo", "Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile", "foo"}, res)

	res = dedupePaths([]string{"foo/bar/baz", "foo/bara", "foo/bar/bax", "foo/bar"})
	require.Equal(t, []string{"foo/bar", "foo/bara"}, res)
}

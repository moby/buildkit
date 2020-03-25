package llb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelativeWd(t *testing.T) {
	st := Scratch().Dir("foo")
	require.Equal(t, getDirHelper(t, st), "/foo")

	st = st.Dir("bar")
	require.Equal(t, getDirHelper(t, st), "/foo/bar")

	st = st.Dir("..")
	require.Equal(t, getDirHelper(t, st), "/foo")

	st = st.Dir("/baz")
	require.Equal(t, getDirHelper(t, st), "/baz")

	st = st.Dir("../../..")
	require.Equal(t, getDirHelper(t, st), "/")
}

func getDirHelper(t *testing.T, s State) string {
	t.Helper()
	v, err := s.GetDir(context.TODO())
	require.NoError(t, err)
	return v
}

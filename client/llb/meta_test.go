package llb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelativeWd(t *testing.T) {
	st := Scratch().Dir("foo")
	assert.Equal(t, getDirHelper(t, st), filepath.FromSlash("/foo"))

	st = st.Dir("bar")
	assert.Equal(t, getDirHelper(t, st), filepath.FromSlash("/foo/bar"))

	st = st.Dir("..")
	assert.Equal(t, getDirHelper(t, st), filepath.FromSlash("/foo"))

	st = st.Dir("/baz")
	assert.Equal(t, getDirHelper(t, st), filepath.FromSlash("/baz"))

	st = st.Dir("../../..")
	assert.Equal(t, getDirHelper(t, st), filepath.FromSlash("/"))
}

func getDirHelper(t *testing.T, s State) string {
	t.Helper()
	v, err := s.GetDir(context.TODO())
	require.NoError(t, err)
	return v
}

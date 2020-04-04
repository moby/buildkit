package llb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateMeta(t *testing.T) {
	t.Parallel()

	s := Image("foo")
	s = s.AddEnv("BAR", "abc").Dir("/foo/bar")

	v, ok := getEnvHelper(t, s, "BAR")
	assert.True(t, ok)
	assert.Equal(t, "abc", v)

	assert.Equal(t, "/foo/bar", getDirHelper(t, s))

	s2 := Image("foo2")
	s2 = s2.AddEnv("BAZ", "def").Reset(s)

	_, ok = getEnvHelper(t, s2, "BAZ")
	assert.False(t, ok)

	v, ok = getEnvHelper(t, s2, "BAR")
	assert.True(t, ok)
	assert.Equal(t, "abc", v)
}

func TestFormattingPatterns(t *testing.T) {
	t.Parallel()

	s := Image("foo")
	s = s.AddEnv("FOO", "ab%sc").Dir("/foo/bar%d")

	v, ok := getEnvHelper(t, s, "FOO")
	assert.True(t, ok)
	assert.Equal(t, "ab%sc", v)

	assert.Equal(t, "/foo/bar%d", getDirHelper(t, s))

	s2 := Image("foo")
	s2 = s2.AddEnvf("FOO", "ab%sc", "__").Dirf("/foo/bar%d", 1)

	v, ok = getEnvHelper(t, s2, "FOO")
	assert.True(t, ok)
	assert.Equal(t, "ab__c", v)

	assert.Equal(t, "/foo/bar1", getDirHelper(t, s2))
}

func getEnvHelper(t *testing.T, s State, k string) (string, bool) {
	t.Helper()
	v, ok, err := s.GetEnv(context.TODO(), k)
	require.NoError(t, err)
	return v, ok
}

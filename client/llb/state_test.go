package llb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStateMeta(t *testing.T) {
	t.Parallel()

	s := Image("foo")
	s = s.AddEnv("BAR", "abc").Dir("/foo/bar")

	v, ok := s.GetEnv("BAR")
	assert.True(t, ok)
	assert.Equal(t, "abc", v)

	assert.Equal(t, "/foo/bar", s.GetDir())

	s2 := Image("foo2")
	s2 = s2.AddEnv("BAZ", "def").Reset(s)

	_, ok = s2.GetEnv("BAZ")
	assert.False(t, ok)

	v, ok = s2.GetEnv("BAR")
	assert.True(t, ok)
	assert.Equal(t, "abc", v)
}

func TestFormattingPatterns(t *testing.T) {
	t.Parallel()

	s := Image("foo")
	s = s.AddEnv("FOO", "ab%sc").Dir("/foo/bar%d")

	v, ok := s.GetEnv("FOO")
	assert.True(t, ok)
	assert.Equal(t, "ab%sc", v)

	assert.Equal(t, "/foo/bar%d", s.GetDir())

	s2 := Image("foo")
	s2 = s2.AddEnvf("FOO", "ab%sc", "__").Dirf("/foo/bar%d", 1)

	v, ok = s2.GetEnv("FOO")
	assert.True(t, ok)
	assert.Equal(t, "ab__c", v)

	assert.Equal(t, "/foo/bar1", s2.GetDir())
}

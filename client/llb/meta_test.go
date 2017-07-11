package llb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultMeta(t *testing.T) {
	m := NewMeta()
	path, ok := m.Env("PATH")
	assert.True(t, ok)
	assert.NotEmpty(t, path)

	cwd := m.Dir()
	assert.NotEmpty(t, cwd)
}

func TestReset(t *testing.T) {
	m := NewMeta()
	wd := m.Dir()
	path, _ := m.Env("PATH")
	m, _ = dir("/foo")(m)
	m, _ = addEnv("FOO", "bar")(m)

	m, _ = reset(nil)(m)
	assert.Equal(t, wd, m.Dir())
	path2, _ := m.Env("PATH")
	assert.Equal(t, path, path2)
}

func TestEnv(t *testing.T) {
	m := NewMeta()
	m, _ = addEnv("FOO", "bar")(m)
	m2, _ := addEnv("FOO", "baz")(m)
	m2, _ = addEnv("BAR", "abc")(m2)

	v, ok := m.Env("FOO")
	assert.True(t, ok)
	assert.Equal(t, "bar", v)

	_, ok = m.Env("BAR")
	assert.False(t, ok)

	v, ok = m2.Env("FOO")
	assert.True(t, ok)
	assert.Equal(t, "baz", v)

	v, ok = m2.Env("BAR")
	assert.True(t, ok)
	assert.Equal(t, "abc", v)
}

func TestShlex(t *testing.T) {
	m, err := shlexf("echo foo")(Meta{})
	assert.Nil(t, err)
	assert.Equal(t, []string{"echo", "foo"}, m.Args())

	_, err = shlexf("echo \"foo")(Meta{})
	assert.Error(t, err)
}

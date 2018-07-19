package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/stretchr/testify/assert"
)

func TestDockerfileParsing(t *testing.T) {
	t.Parallel()
	df := `FROM busybox
ENV FOO bar
COPY f1 f2 /sub/
RUN ls -l
`
	_, _, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.NoError(t, err)

	df = `FROM busybox AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.NoError(t, err)

	df = `FROM busybox AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Target: "Foo",
	})
	assert.NoError(t, err)

	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Target: "nosuch",
	})
	assert.Error(t, err)

	df = `FROM busybox
	ADD http://github.com/moby/buildkit/blob/master/README.md /
		`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.NoError(t, err)

	df = `FROM busybox
	COPY http://github.com/moby/buildkit/blob/master/README.md /
		`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.EqualError(t, err, "source can't be a URL for COPY")

	df = `FROM "" AS foo`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.Error(t, err)

	df = `FROM ${BLANK} AS foo`
	_, _, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	assert.Error(t, err)
}

func TestAddEnv(t *testing.T) {
	// k exists in env as key
	// override = false
	env := []string{"key1=val1", "key2=val2"}
	result := addEnv(env, "key1", "value1", false)
	assert.Equal(t, []string{"key1=val1", "key2=val2"}, result)

	// k exists in env as key
	// override = true
	env = []string{"key1=val1", "key2=val2"}
	result = addEnv(env, "key1", "value1", true)
	assert.Equal(t, []string{"key1=value1", "key2=val2"}, result)

	// k does not exist in env as key
	// override = false
	env = []string{"key1=val1", "key2=val2"}
	result = addEnv(env, "key3", "val3", false)
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3=val3"}, result)

	// k does not exist in env as key
	// override = true
	env = []string{"key1=val1", "key2=val2"}
	result = addEnv(env, "key3", "val3", true)
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3=val3"}, result)

	// env has same keys
	// override = false
	env = []string{"key1=val1", "key1=val2"}
	result = addEnv(env, "key1", "value1", false)
	assert.Equal(t, []string{"key1=val1", "key1=val2"}, result)

	// env has same keys
	// override = true
	env = []string{"key1=val1", "key1=val2"}
	result = addEnv(env, "key1", "value1", true)
	assert.Equal(t, []string{"key1=value1", "key1=val2"}, result)

	// k matches with key only string in env
	// override = false
	env = []string{"key1=val1", "key2=val2", "key3"}
	result = addEnv(env, "key3", "val3", false)
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3"}, result)

	// k matches with key only string in env
	// override = true
	env = []string{"key1=val1", "key2=val2", "key3"}
	result = addEnv(env, "key3", "val3", true)
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3=val3"}, result)
}

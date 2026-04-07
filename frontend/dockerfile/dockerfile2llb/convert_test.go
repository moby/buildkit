package dockerfile2llb

import (
	"context"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/appcontext"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toEnvMap(args []instructions.KeyValuePairOptional, env []string) map[string]string {
	envs := shell.EnvsFromSlice(env)
	m := make(map[string]string)

	for _, k := range envs.Keys() {
		m[k], _ = envs.Get(k)
	}

	for _, arg := range args {
		// If key already exists, keep previous value.
		if _, ok := m[arg.Key]; ok {
			continue
		}
		if arg.Value != nil {
			m[arg.Key] = arg.ValueString()
		}
	}
	return m
}

func TestDockerfileParsing(t *testing.T) {
	t.Parallel()
	df := `FROM scratch
ENV FOO bar
COPY f1 f2 /sub/
RUN ls -l
`
	_, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)

	df = `FROM scratch AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)

	df = `FROM scratch AS foo
ENV FOO bar
FROM foo
COPY --from=foo f1 /
COPY --from=0 f2 /
	`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Config: dockerui.Config{
			Target: "Foo",
		},
	})
	require.NoError(t, err)

	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
		Config: dockerui.Config{
			Target: "nosuch",
		},
	})
	require.Error(t, err)

	df = `FROM scratch
	ADD http://github.com/moby/buildkit/blob/master/README.md /
		`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)

	df = `FROM scratch
	COPY http://github.com/moby/buildkit/blob/master/README.md /
		`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.EqualError(t, err, "source can't be a URL for COPY")

	df = `FROM "" AS foo`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.Error(t, err)

	df = `FROM ${BLANK} AS foo`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.Error(t, err)
}

func TestDockerfileParsingMarshal(t *testing.T) {
	t.Parallel()
	df := `FROM scratch
ENV FOO bar
COPY f1 f2 /sub/
RUN ls -l
`
	res, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)

	_, err = res.State.Marshal(context.TODO())
	require.NoError(t, err)
}

func TestAddEnv(t *testing.T) {
	// k exists in env as key
	// override = true
	env := []string{"key1=val1", "key2=val2"}
	result := addEnv(env, "key1", "value1")
	assert.Equal(t, []string{"key1=value1", "key2=val2"}, result)

	// k does not exist in env as key
	// override = true
	env = []string{"key1=val1", "key2=val2"}
	result = addEnv(env, "key3", "val3")
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3=val3"}, result)

	// env has same keys
	// override = true
	env = []string{"key1=val1", "key1=val2"}
	result = addEnv(env, "key1", "value1")
	assert.Equal(t, []string{"key1=value1", "key1=val2"}, result)

	// k matches with key only string in env
	// override = true
	env = []string{"key1=val1", "key2=val2", "key3"}
	result = addEnv(env, "key3", "val3")
	assert.Equal(t, []string{"key1=val1", "key2=val2", "key3=val3"}, result)
}

func TestParseKeyValue(t *testing.T) {
	k, v := parseKeyValue("key=val")
	assert.Equal(t, "key", k)
	assert.Equal(t, "val", v)

	k, v = parseKeyValue("key=")
	assert.Equal(t, "key", k)
	assert.Equal(t, "", v)

	k, v = parseKeyValue("key")
	assert.Equal(t, "key", k)
	assert.Equal(t, "", v)
}

func TestToEnvList(t *testing.T) {
	// args has no duplicated key with env
	v := "val2"
	args := []instructions.KeyValuePairOptional{{Key: "key2", Value: &v}}
	env := []string{"key1=val1"}
	result := toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": "val1", "key2": "val2"}, result)

	// value of args is nil
	args = []instructions.KeyValuePairOptional{{Key: "key2", Value: nil}}
	env = []string{"key1=val1"}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": "val1"}, result)

	// args has duplicated key with env
	v = "val2"
	args = []instructions.KeyValuePairOptional{{Key: "key1", Value: &v}}
	env = []string{"key1=val1"}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": "val1"}, result)

	v = "val2"
	args = []instructions.KeyValuePairOptional{{Key: "key1", Value: &v}}
	env = []string{"key1="}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": ""}, result)

	v = "val2"
	args = []instructions.KeyValuePairOptional{{Key: "key1", Value: &v}}
	env = []string{"key1"}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": ""}, result)

	// env has duplicated keys
	v = "val2"
	args = []instructions.KeyValuePairOptional{{Key: "key2", Value: &v}}
	env = []string{"key1=val1", "key1=val1_2"}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": "val1_2", "key2": "val2"}, result)

	// args has duplicated keys
	v1 := "v1"
	v2 := "v2"
	args = []instructions.KeyValuePairOptional{{Key: "key2", Value: &v1}, {Key: "key2", Value: &v2}}
	env = []string{"key1=val1"}
	result = toEnvMap(args, env)
	assert.Equal(t, map[string]string{"key1": "val1", "key2": "v1"}, result)
}

func TestProxyEnvFromBuildArgsDeterministicOrder(t *testing.T) {
	pe := proxyEnvFromBuildArgs(map[string]string{
		"ALL_PROXY":  "all-upper",
		"all_proxy":  "all-lower",
		"HTTP_PROXY": "http-upper",
		"http_proxy": "http-lower",
		"NO_PROXY":   "no-proxy",
	})
	require.NotNil(t, pe)
	require.Equal(t, &llb.ProxyEnv{
		HTTPProxy: "http-lower",
		NoProxy:   "no-proxy",
		AllProxy:  "all-lower",
	}, pe)
}

func TestProxyEnvFromBuildArgsNilWhenNoProxyArgs(t *testing.T) {
	pe := proxyEnvFromBuildArgs(map[string]string{
		"FOO": "bar",
	})
	require.Nil(t, pe)
}

func TestDockerfileCircularDependencies(t *testing.T) {
	// single stage depends on itself
	df := `FROM busybox AS stage0
COPY --from=stage0 f1 /sub/
`
	_, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.EqualError(t, err, "circular dependency detected on stage: stage0")

	// multiple stages with circular dependency
	df = `FROM busybox AS stage0
COPY --from=stage2 f1 /sub/
FROM busybox AS stage1
COPY --from=stage0 f2 /sub/
FROM busybox AS stage2
COPY --from=stage1 f2 /sub/
`
	_, err = Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.EqualError(t, err, "circular dependency detected on stage: stage0")
}

func TestBaseImageConfig(t *testing.T) {
	df := `FROM --platform=linux/amd64 busybox:1.36.1@sha256:6d9ac9237a84afe1516540f40a0fafdc86859b2141954b4d643af7066d598b74 AS foo
RUN echo foo

# the source image of bar is busybox, not foo
FROM foo AS bar
RUN echo bar
`
	res, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{})
	require.NoError(t, err)
	t.Logf("baseImg=%+v", res.BaseImage)
	assert.Equal(t, []digest.Digest{"sha256:2e112031b4b923a873c8b3d685d48037e4d5ccd967b658743d93a6e56c3064b9"}, res.BaseImage.RootFS.DiffIDs)
	assert.Equal(t, "2024-01-17 21:49:12 +0000 UTC", res.BaseImage.Created.String())
}

func TestDispatchHealthcheckHistory(t *testing.T) {
	hc := &instructions.HealthCheckCommand{
		Health: &dockerspec.HealthcheckConfig{
			Test:          []string{"bin", "-c", "exit 0"},
			Interval:      1 * time.Second,
			Timeout:       10 * time.Second,
			StartPeriod:   3 * time.Second,
			StartInterval: 100 * time.Millisecond,
			Retries:       5,
		},
	}

	d := &dispatchState{}
	err := dispatchHealthcheck(d, hc, &linter.Linter{})
	require.NoError(t, err)
	want := `HEALTHCHECK {Test:[bin -c exit 0] Interval:1s Timeout:10s StartPeriod:3s StartInterval:100ms Retries:5}`
	require.Equal(t, want, d.image.History[0].CreatedBy)
}

package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarsalBuildDefinitionSLSA1(t *testing.T) {
	inp := `{
"buildType": "btype1",
"externalParameters": {
	"configSource": {},
	"request": {}
},
"internalParameters": {
	"builderPlatform": "linux/amd64",
		"foo": "bar",
		"abc": 123,
		"def": {"one": 1}
	}
}`

	var def ProvenanceBuildDefinitionSLSA1
	err := json.Unmarshal([]byte(inp), &def)
	require.NoError(t, err)

	require.Equal(t, "btype1", def.BuildType)
	require.Equal(t, "linux/amd64", def.InternalParameters.BuilderPlatform)
	require.Equal(t, "bar", def.InternalParameters.ProvenanceCustomEnv["foo"])
	require.InEpsilon(t, float64(123), def.InternalParameters.ProvenanceCustomEnv["abc"], 0.001)
	require.Equal(t, map[string]any{"one": float64(1)}, def.InternalParameters.ProvenanceCustomEnv["def"])

	out, err := json.Marshal(def)
	require.NoError(t, err)

	require.JSONEq(t, inp, string(out))
}

func TestMarshalInvocation(t *testing.T) {
	inp := `{
	"configSource": {
		"uri": "git+https://github.com/example/repo.git"
	},
	"parameters": {
		"frontend": "dockerfile.v0"
	},
	"environment": {
		"platform": "linux/amd64",
		"buildkit": "v0.10.3",
		"custom": {
			"foo": "bar"
		},
		"bar": [1,2,3]
	}
}`

	var inv ProvenanceInvocationSLSA02
	err := json.Unmarshal([]byte(inp), &inv)
	require.NoError(t, err)

	require.Equal(t, "git+https://github.com/example/repo.git", inv.ConfigSource.URI)
	require.Equal(t, "dockerfile.v0", inv.Parameters.Frontend)
	require.Equal(t, "linux/amd64", inv.Environment.Platform)
	require.Equal(t, "v0.10.3", inv.Environment.ProvenanceCustomEnv["buildkit"])
	require.Equal(t, "bar", inv.Environment.ProvenanceCustomEnv["custom"].(map[string]any)["foo"])
	require.Equal(t, []any{float64(1), float64(2), float64(3)}, inv.Environment.ProvenanceCustomEnv["bar"])
	out, err := json.Marshal(inv)
	require.NoError(t, err)

	require.JSONEq(t, inp, string(out))
}

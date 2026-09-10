package epoch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBuildArgs(t *testing.T) {
	t.Parallel()

	v, ok := ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: "1700000601"})
	require.Truef(t, ok, "expected numeric SOURCE_DATE_EPOCH to be forwarded")
	require.Equalf(t, "1700000601", v, "expected numeric SOURCE_DATE_EPOCH to be forwarded")

	_, ok = ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: "context"})
	require.False(t, ok, "expected SOURCE_DATE_EPOCH=context to stay frontend-only")

	v, ok = ParseBuildArgs(map[string]string{frontendSourceDateEpochArg: ""})
	require.True(t, ok, "expected empty SOURCE_DATE_EPOCH to remain a valid exporter override")
	require.Empty(t, v, "expected empty SOURCE_DATE_EPOCH to remain a valid exporter override")
}

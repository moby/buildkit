package compat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupportedCompatibilityVersions(t *testing.T) {
	require.Equal(t, []int{10, 20, 30}, SupportedCompatibilityVersions())
	require.Equal(t, 30, CompatibilityVersionCurrent)
}

func TestValidateCompatibilityVersion(t *testing.T) {
	require.NoError(t, ValidateCompatibilityVersion(CompatibilityVersion013))
	require.NoError(t, ValidateCompatibilityVersion(CompatibilityVersionCurrent))
	require.ErrorContains(t, ValidateCompatibilityVersion(11), "unsupported compatibility-version 11")
	require.ErrorContains(t, ValidateCompatibilityVersion(40), "upgrade buildkit")
}

package nvidia

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	in := `NVRM version: NVIDIA UNIX aarch64 Kernel Module  550.120  Fri Sep 13 11:01:10 UTC 2024`
	out, err := parseVersion(in)
	require.NoError(t, err)
	require.Equal(t, "550.120", out)

	in = `NVRM version: NVIDIA UNIX Open Kernel Module for aarch64  550.144.03  Release Build  (dvs-builder@U16-I2-C01-12-4)  Mon Dec 30 17:33:24 UTC 2024
GCC version:  gcc version 12.3.0 (Ubuntu 12.3.0-1ubuntu1~22.04)`
	out, err = parseVersion(in)
	require.NoError(t, err)
	require.Equal(t, "550.144", out)
}

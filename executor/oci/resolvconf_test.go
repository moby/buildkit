package oci

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolvConfNotExist modifies a global variable
// It must not run in parallel.
func TestResolvConfNotExist(t *testing.T) {
	oldResolvconfPath := resolvconfPath
	t.Cleanup(func() {
		resolvconfPath = oldResolvconfPath
	})
	resolvconfPath = func() string {
		return "no-such-file"
	}

	defaultResolvConf := `
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844`

	ctx := context.Background()
	p, err := GetResolvConf(ctx, t.TempDir(), nil, nil)
	require.NoError(t, err)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, string(b), defaultResolvConf)
}

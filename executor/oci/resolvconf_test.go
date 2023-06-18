package oci

import (
	"context"
	"os"
	"testing"

	"github.com/moby/buildkit/executor"
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
	p, clean, err := GetResolvConf(ctx, t.TempDir(), nil, nil, nil)
	require.NoError(t, err)
	defer clean()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, defaultResolvConf, string(b))
}

func TestResolvConfOverrides(t *testing.T) {
	oldResolvconfPath := resolvconfPath
	t.Cleanup(func() {
		resolvconfPath = oldResolvconfPath
	})
	resolvconfPath = func() string {
		return "no-such-file"
	}

	workerDNS := &executor.DNSConfig{
		Nameservers: []string{"1.2.3.4"},
	}

	expected := `nameserver 1.2.3.4
`

	ctx := context.Background()
	p, clean, err := GetResolvConf(ctx, t.TempDir(), nil, workerDNS, nil)
	require.NoError(t, err)
	defer clean()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, expected, string(b))
	clean()
	_, err = os.Stat(p)
	require.NoError(t, err) // no custom DNS = nothing to clean up

	customDNS := &executor.DNSConfig{
		SearchDomains: []string{"custom-domain"},
	}

	// NB: custom DNS overrides worker DNS completely. in practice it will have
	// been merged with the worker DNS already.
	expected = `search custom-domain

nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844`

	p, clean, err = GetResolvConf(ctx, t.TempDir(), nil, workerDNS, customDNS)
	require.NoError(t, err)
	defer clean()
	b, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, expected, string(b))
	clean()
	_, err = os.Stat(p)
	require.ErrorIs(t, err, os.ErrNotExist)
}

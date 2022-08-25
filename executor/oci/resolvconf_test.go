package oci

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/docker/docker/libnetwork/resolvconf"
	"github.com/stretchr/testify/require"
)

// TestResolvConfNotExist modifies a global variable
// It must not run in parallel.
func TestResolvConfNotExist(t *testing.T) {
	oldResolvconfGet := resolvconfGet
	defer func() {
		resolvconfGet = oldResolvconfGet
	}()
	resolvconfGet = func() (*resolvconf.File, error) {
		return nil, os.ErrNotExist
	}

	defaultResolvConf := `
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844`

	ctx := context.Background()
	p, cleanup, err := GetResolvConf(ctx, t.TempDir(), nil, nil)
	require.NoError(t, err)
	defer cleanup()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, string(b), defaultResolvConf)
	cleanup()
	_, err = os.ReadFile(p)
	require.Error(t, err)
}

func TestResolvConfFilterLocal(t *testing.T) {
	oldResolvconfGet := resolvconfGet
	defer func() {
		resolvconfGet = oldResolvconfGet
	}()
	resolvconfGet = func() (*resolvconf.File, error) {
		return &resolvconf.File{
			Content: []byte(strings.Join([]string{
				"nameserver 127.0.0.1",
				"nameserver 192.168.1.1",
				"nameserver 1.1.1.1",
				"search example.com",
				"options ndots:1",
			}, "\n")),
			Hash: "doesnt-matter",
		}, nil
	}

	defaultResolvConf := `nameserver 192.168.1.1
nameserver 1.1.1.1
search example.com
options ndots:1`

	ctx := context.Background()
	p, cleanup, err := GetResolvConf(ctx, t.TempDir(), nil, nil)
	require.NoError(t, err)
	defer cleanup()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, string(b), defaultResolvConf)
	cleanup()
	_, err = os.ReadFile(p)
	require.Error(t, err)
}

func TestResolvConfOverride(t *testing.T) {
	oldResolvconfGet := resolvconfGet
	defer func() {
		resolvconfGet = oldResolvconfGet
	}()
	resolvconfGet = func() (*resolvconf.File, error) {
		return &resolvconf.File{
			Content: []byte(strings.Join([]string{
				"nameserver 127.0.0.1",
				"nameserver 192.168.1.1",
				"nameserver 1.1.1.1",
				"search example.com",
				"options ndots:1",
			}, "\n")),
			Hash: "doesnt-matter",
		}, nil
	}

	overriddenResolvConf := `search example.org
nameserver 1.2.3.4
nameserver 5.6.7.8
options ndots:2
`

	ctx := context.Background()
	p, cleanup, err := GetResolvConf(ctx, t.TempDir(), nil, &DNSConfig{
		Nameservers:   []string{"1.2.3.4", "5.6.7.8"},
		Options:       []string{"ndots:2"},
		SearchDomains: []string{"example.org"},
	})
	require.NoError(t, err)
	defer cleanup()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, overriddenResolvConf, string(b))
	cleanup()
	_, err = os.ReadFile(p)
	require.Error(t, err)
}

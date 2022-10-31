package oci

import (
	"context"
	"fmt"
	"github.com/docker/docker/libnetwork/resolvconf"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

const defaultResolvConf = `
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844`

const dnsOption = `
options ndots:0`

const localDNSResolvConf = `
nameserver 127.0.0.11
options ndots:0`

const regularResolvConf = `
# DNS requests are forwarded to the host. DHCP DNS options are ignored.
nameserver 192.168.65.5`

// TestResolvConf modifies a global variable
// It must not run in parallel.
func TestResolvConf(t *testing.T) {
	cases := []struct {
		name               string
		resolvconfGetFile  *resolvconf.File
		resolvconfGetError error
		execution          int
		hostNetMode        []bool
		expected           []string
	}{
		{
			name:               "TestResolvConfNotExist",
			resolvconfGetFile:  nil,
			resolvconfGetError: os.ErrNotExist,
			execution:          1,
			hostNetMode:        []bool{false},
			expected:           []string{defaultResolvConf},
		},
		{
			name:               "TestNetModeIsHostResolvConfNotExist",
			resolvconfGetFile:  nil,
			resolvconfGetError: os.ErrNotExist,
			execution:          1,
			hostNetMode:        []bool{true},
			expected:           []string{defaultResolvConf},
		},
		{
			name: "TestNetModeIsHostWithoutLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(regularResolvConf),
			},
			resolvconfGetError: nil,
			execution:          1,
			hostNetMode:        []bool{true},
			expected:           []string{regularResolvConf},
		},
		{
			name: "TestNetModeIsHostWithLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(localDNSResolvConf),
			},
			resolvconfGetError: nil,
			execution:          1,
			hostNetMode:        []bool{true},
			expected:           []string{localDNSResolvConf},
		},
		{
			name: "TestNetModeNotHostWithoutLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(regularResolvConf),
			},
			resolvconfGetError: nil,
			execution:          1,
			hostNetMode:        []bool{false},
			expected:           []string{regularResolvConf},
		},
		{
			name: "TestNetModeNotHostWithLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(localDNSResolvConf),
			},
			resolvconfGetError: nil,
			execution:          1,
			hostNetMode:        []bool{false},
			expected:           []string{fmt.Sprintf("%s%s", dnsOption, defaultResolvConf)},
		},
		{
			name: "TestRegenerateResolvconfToRemoveLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(localDNSResolvConf),
			},
			resolvconfGetError: nil,
			execution:          2,
			hostNetMode:        []bool{true, false},
			expected: []string{
				localDNSResolvConf,
				fmt.Sprintf("%s%s", dnsOption, defaultResolvConf),
			},
		},
		{
			name: "TestRegenerateResolvconfToAddLocalDNS",
			resolvconfGetFile: &resolvconf.File{
				Content: []byte(localDNSResolvConf),
			},
			resolvconfGetError: nil,
			execution:          2,
			hostNetMode:        []bool{false, true},
			expected: []string{
				fmt.Sprintf("%s%s", dnsOption, defaultResolvConf),
				localDNSResolvConf,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldResolvconfGet := resolvconfGet
			defer func() {
				resolvconfGet = oldResolvconfGet
			}()
			resolvconfGet = func() (*resolvconf.File, error) {
				return tc.resolvconfGetFile, tc.resolvconfGetError
			}

			ctx := context.Background()
			tempDir := t.TempDir()

			for i := 0; i < tc.execution; i++ {
				if i > 0 {
					time.Sleep(100 * time.Millisecond)
				}

				p, err := GetResolvConf(ctx, tempDir, nil, nil, tc.hostNetMode[i])
				require.NoError(t, err)
				b, err := os.ReadFile(p)
				require.NoError(t, err)
				require.Equal(t, tc.expected[i], string(b))
			}
		})
	}
}

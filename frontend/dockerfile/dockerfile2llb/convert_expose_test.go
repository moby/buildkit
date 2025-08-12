package dockerfile2llb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePortSpecEmptyContainerPort(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		expError string
	}{
		{
			name:     "empty spec",
			spec:     "",
			expError: `invalid port: "": no port specified`,
		},
		{
			name:     "empty container port",
			spec:     `0.0.0.0:1234-1235:/tcp`,
			expError: `invalid port: "0.0.0.0:1234-1235:/tcp": no port specified`,
		},
		{
			name:     "empty container port and proto",
			spec:     `0.0.0.0:1234-1235:`,
			expError: `invalid port: "0.0.0.0:1234-1235:": no port specified`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePortSpec(tc.spec)
			if tc.expError != "" {
				assert.EqualError(t, err, tc.expError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParsePortSpecFull(t *testing.T) {
	exposedPorts, err := parsePortSpec("0.0.0.0:1234-1235:3333-3334/tcp")
	require.NoError(t, err)
	assert.Equal(t, []string{"3333/tcp", "3334/tcp"}, exposedPorts)
}

func TestPartPortSpecIPV6(t *testing.T) {
	type test struct {
		name     string
		spec     string
		expected []string
	}
	cases := []test{
		{
			name:     "square angled IPV6 without host port",
			spec:     "[2001:4860:0:2001::68]::333",
			expected: []string{"333/tcp"},
		},
		{
			name:     "square angled IPV6 with host port",
			spec:     "[::1]:80:80",
			expected: []string{"80/tcp"},
		},
		{
			name:     "IPV6 without host port",
			spec:     "2001:4860:0:2001::68::333",
			expected: []string{"333/tcp"},
		},
		{
			name:     "IPV6 with host port",
			spec:     "::1:80:80",
			expected: []string{"80/tcp"},
		},
		{
			name:     ":: IPV6, without host port",
			spec:     "::::80",
			expected: []string{"80/tcp"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exposedPorts, err := parsePortSpec(c.spec)
			require.NoError(t, err)
			assert.Equal(t, c.expected, exposedPorts)
		})
	}
}

func TestParsePortSpecs(t *testing.T) {
	exposedPorts, err := parsePortSpecs([]string{"1234/tcp", "2345/udp", "3456/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"1234:1234/tcp", "2345:2345/udp", "3456:3456/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"0.0.0.0:1234:1234/tcp", "0.0.0.0:2345:2345/udp", "0.0.0.0:3456:3456/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	_, err = parsePortSpecs([]string{"localhost:1234:1234/tcp"})
	assert.Error(t, err, "Received no error while trying to parse a hostname instead of ip")
}

func TestParsePortSpecsWithRange(t *testing.T) {
	exposedPorts, err := parsePortSpecs([]string{"1234-1236/tcp", "2345-2347/udp", "3456-3458/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"1234-1236:1234-1236/tcp", "2345-2347:2345-2347/udp", "3456-3458:3456-3458/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"0.0.0.0:1234-1236:1234-1236/tcp", "0.0.0.0:2345-2347:2345-2347/udp", "0.0.0.0:3456-3458:3456-3458/sctp"})
	require.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	_, err = parsePortSpecs([]string{"localhost:1234-1236:1234-1236/tcp"})
	assert.Error(t, err, "Received no error while trying to parse a hostname instead of ip")
}

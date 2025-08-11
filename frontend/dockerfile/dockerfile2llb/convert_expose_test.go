package dockerfile2llb

import (
	"reflect"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
)

func TestParsePortSpecFull(t *testing.T) {
	portMappings, err := parsePortSpec("0.0.0.0:1234-1235:3333-3334/tcp")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	expected := []nat.PortMapping{
		{
			Port: "3333/tcp",
			Binding: nat.PortBinding{
				HostIP:   "0.0.0.0",
				HostPort: "1234",
			},
		},
		{
			Port: "3334/tcp",
			Binding: nat.PortBinding{
				HostIP:   "0.0.0.0",
				HostPort: "1235",
			},
		},
	}

	if !reflect.DeepEqual(expected, portMappings) {
		t.Fatalf("wrong port mappings: got=%v, want=%v", portMappings, expected)
	}
}

func TestPartPortSpecIPV6(t *testing.T) {
	type test struct {
		name     string
		spec     string
		expected []nat.PortMapping
	}
	cases := []test{
		{
			name: "square angled IPV6 without host port",
			spec: "[2001:4860:0:2001::68]::333",
			expected: []nat.PortMapping{
				{
					Port: "333/tcp",
					Binding: nat.PortBinding{
						HostIP:   "2001:4860:0:2001::68",
						HostPort: "",
					},
				},
			},
		},
		{
			name: "square angled IPV6 with host port",
			spec: "[::1]:80:80",
			expected: []nat.PortMapping{
				{
					Port: "80/tcp",
					Binding: nat.PortBinding{
						HostIP:   "::1",
						HostPort: "80",
					},
				},
			},
		},
		{
			name: "IPV6 without host port",
			spec: "2001:4860:0:2001::68::333",
			expected: []nat.PortMapping{
				{
					Port: "333/tcp",
					Binding: nat.PortBinding{
						HostIP:   "2001:4860:0:2001::68",
						HostPort: "",
					},
				},
			},
		},
		{
			name: "IPV6 with host port",
			spec: "::1:80:80",
			expected: []nat.PortMapping{
				{
					Port: "80/tcp",
					Binding: nat.PortBinding{
						HostIP:   "::1",
						HostPort: "80",
					},
				},
			},
		},
		{
			name: ":: IPV6, without host port",
			spec: "::::80",
			expected: []nat.PortMapping{
				{
					Port: "80/tcp",
					Binding: nat.PortBinding{
						HostIP:   "::",
						HostPort: "",
					},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			portMappings, err := parsePortSpec(c.spec)
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			if !reflect.DeepEqual(c.expected, portMappings) {
				t.Fatalf("wrong port mappings: got=%v, want=%v", portMappings, c.expected)
			}
		})
	}
}

func TestParsePortSpecs(t *testing.T) {
	exposedPorts, err := parsePortSpecs([]string{"1234/tcp", "2345/udp", "3456/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"1234:1234/tcp", "2345:2345/udp", "3456:3456/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"0.0.0.0:1234:1234/tcp", "0.0.0.0:2345:2345/udp", "0.0.0.0:3456:3456/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "2345/udp", "3456/sctp"}, exposedPorts)

	_, err = parsePortSpecs([]string{"localhost:1234:1234/tcp"})
	assert.Error(t, err, "Received no error while trying to parse a hostname instead of ip")
}

func TestParsePortSpecsWithRange(t *testing.T) {
	exposedPorts, err := parsePortSpecs([]string{"1234-1236/tcp", "2345-2347/udp", "3456-3458/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"1234-1236:1234-1236/tcp", "2345-2347:2345-2347/udp", "3456-3458:3456-3458/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	exposedPorts, err = parsePortSpecs([]string{"0.0.0.0:1234-1236:1234-1236/tcp", "0.0.0.0:2345-2347:2345-2347/udp", "0.0.0.0:3456-3458:3456-3458/sctp"})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1234/tcp", "1235/tcp", "1236/tcp", "2345/udp", "2346/udp", "2347/udp", "3456/sctp", "3457/sctp", "3458/sctp"}, exposedPorts)

	_, err = parsePortSpecs([]string{"localhost:1234-1236:1234-1236/tcp"})
	assert.Error(t, err, "Received no error while trying to parse a hostname instead of ip")
}

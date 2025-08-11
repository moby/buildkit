package dockerfile2llb

import (
	"reflect"
	"testing"

	"github.com/docker/go-connections/nat"
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
	var (
		portMap    map[nat.Port]struct{}
		bindingMap map[nat.Port][]nat.PortBinding
		err        error
	)

	portMap, bindingMap, err = parsePortSpecs([]string{"1234/tcp", "2345/udp", "3456/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1234/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2345/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		if len(bindings) != 1 {
			t.Fatalf("%s should have exactly one binding", portSpec)
		}

		if bindings[0].HostIP != "" {
			t.Fatalf("HostIP should not be set for %s", portSpec)
		}

		if bindings[0].HostPort != "" {
			t.Fatalf("HostPort should not be set for %s", portSpec)
		}
	}

	portMap, bindingMap, err = parsePortSpecs([]string{"1234:1234/tcp", "2345:2345/udp", "3456:3456/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1234/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2345/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		_, port := splitProtoPort(string(portSpec))

		if len(bindings) != 1 {
			t.Fatalf("%s should have exactly one binding", portSpec)
		}

		if bindings[0].HostIP != "" {
			t.Fatalf("HostIP should not be set for %s", portSpec)
		}

		if bindings[0].HostPort != port {
			t.Fatalf("HostPort should be %s for %s", port, portSpec)
		}
	}

	portMap, bindingMap, err = parsePortSpecs([]string{"0.0.0.0:1234:1234/tcp", "0.0.0.0:2345:2345/udp", "0.0.0.0:3456:3456/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1234/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2345/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		_, port := splitProtoPort(string(portSpec))

		if len(bindings) != 1 {
			t.Fatalf("%s should have exactly one binding", portSpec)
		}

		if bindings[0].HostIP != "0.0.0.0" {
			t.Fatalf("HostIP is not 0.0.0.0 for %s", portSpec)
		}

		if bindings[0].HostPort != port {
			t.Fatalf("HostPort should be %s for %s", port, portSpec)
		}
	}

	_, _, err = parsePortSpecs([]string{"localhost:1234:1234/tcp"})

	if err == nil {
		t.Fatal("Received no error while trying to parse a hostname instead of ip")
	}
}

func TestParsePortSpecsWithRange(t *testing.T) {
	var (
		portMap    map[nat.Port]struct{}
		bindingMap map[nat.Port][]nat.PortBinding
		err        error
	)

	portMap, bindingMap, err = parsePortSpecs([]string{"1234-1236/tcp", "2345-2347/udp", "3456-3458/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1235/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2346/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		if len(bindings) != 1 {
			t.Fatalf("%s should have exactly one binding", portSpec)
		}

		if bindings[0].HostIP != "" {
			t.Fatalf("HostIP should not be set for %s", portSpec)
		}

		if bindings[0].HostPort != "" {
			t.Fatalf("HostPort should not be set for %s", portSpec)
		}
	}

	portMap, bindingMap, err = parsePortSpecs([]string{"1234-1236:1234-1236/tcp", "2345-2347:2345-2347/udp", "3456-3458:3456-3458/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1235/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2346/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		_, port := splitProtoPort(string(portSpec))
		if len(bindings) != 1 {
			t.Fatalf("%s should have exactly one binding", portSpec)
		}

		if bindings[0].HostIP != "" {
			t.Fatalf("HostIP should not be set for %s", portSpec)
		}

		if bindings[0].HostPort != port {
			t.Fatalf("HostPort should be %s for %s", port, portSpec)
		}
	}

	portMap, bindingMap, err = parsePortSpecs([]string{"0.0.0.0:1234-1236:1234-1236/tcp", "0.0.0.0:2345-2347:2345-2347/udp", "0.0.0.0:3456-3458:3456-3458/sctp"})
	if err != nil {
		t.Fatalf("Error while processing ParsePortSpecs: %s", err)
	}

	if _, ok := portMap["1235/tcp"]; !ok {
		t.Fatal("1234/tcp was not parsed properly")
	}

	if _, ok := portMap["2346/udp"]; !ok {
		t.Fatal("2345/udp was not parsed properly")
	}

	if _, ok := portMap["3456/sctp"]; !ok {
		t.Fatal("3456/sctp was not parsed properly")
	}

	for portSpec, bindings := range bindingMap {
		_, port := splitProtoPort(string(portSpec))
		if len(bindings) != 1 || bindings[0].HostIP != "0.0.0.0" || bindings[0].HostPort != port {
			t.Fatalf("Expect single binding to port %s but found %s", port, bindings)
		}
	}

	_, _, err = parsePortSpecs([]string{"localhost:1234-1236:1234-1236/tcp"})

	if err == nil {
		t.Fatal("Received no error while trying to parse a hostname instead of ip")
	}
}

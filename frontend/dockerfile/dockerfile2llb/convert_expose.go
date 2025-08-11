package dockerfile2llb

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/docker/go-connections/nat"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/pkg/errors"
)

func dispatchExpose(d *dispatchState, c *instructions.ExposeCommand, shlex *shell.Lex) error {
	ports := []string{}
	env := getEnv(d.state)
	for _, p := range c.Ports {
		ps, err := shlex.ProcessWords(p, env)
		if err != nil {
			return err
		}
		ports = append(ports, ps...)
	}
	c.Ports = ports

	ps, err := parsePortSpecs(c.Ports)
	if err != nil {
		return err
	}

	if d.image.Config.ExposedPorts == nil {
		d.image.Config.ExposedPorts = make(map[string]struct{})
	}
	for _, p := range ps {
		d.image.Config.ExposedPorts[p] = struct{}{}
	}

	return commitToHistory(&d.image, fmt.Sprintf("EXPOSE %v", ps), false, nil, d.epoch)
}

// parsePortSpecs receives port specs in the format of [ip:]public:private/proto
// and returns them as a list of "port/proto".
func parsePortSpecs(ports []string) (exposedPorts []string, _ error) {
	for _, p := range ports {
		portMappings, err := parsePortSpec(p)
		if err != nil {
			return nil, err
		}

		for _, pm := range portMappings {
			exposedPorts = append(exposedPorts, string(pm.Port))
		}
	}
	return exposedPorts, nil
}

// splitProtoPort splits a port(range) and protocol, formatted as "<portnum>/[<proto>]"
// "<startport-endport>/[<proto>]". It returns an empty string for both if
// no port(range) is provided. If a port(range) is provided, but no protocol,
// the default ("tcp") protocol is returned.
//
// splitProtoPort does not validate or normalize the returned values.
func splitProtoPort(rawPort string) (proto string, port string) {
	port, proto, _ = strings.Cut(rawPort, "/")
	if port == "" {
		return "", ""
	}
	if proto == "" {
		proto = "tcp"
	}
	return proto, port
}

func validateProto(proto string) error {
	switch proto {
	case "tcp", "udp", "sctp":
		// All good
		return nil
	default:
		return errors.New("invalid proto: " + proto)
	}
}

func splitParts(rawport string) (hostIP, hostPort, containerPort string) {
	parts := strings.Split(rawport, ":")

	switch len(parts) {
	case 1:
		return "", "", parts[0]
	case 2:
		return "", parts[0], parts[1]
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		n := len(parts)
		return strings.Join(parts[:n-2], ":"), parts[n-2], parts[n-1]
	}
}

// parsePortSpec parses a port specification string into a slice of PortMappings
func parsePortSpec(rawPort string) ([]nat.PortMapping, error) {
	ip, hostPort, containerPort := splitParts(rawPort)
	proto, containerPort := splitProtoPort(containerPort)
	proto = strings.ToLower(proto)
	if err := validateProto(proto); err != nil {
		return nil, err
	}

	if ip != "" && ip[0] == '[' {
		// Strip [] from IPV6 addresses
		rawIP, _, err := net.SplitHostPort(ip + ":")
		if err != nil {
			return nil, errors.Wrapf(err, "invalid IP address %v", ip)
		}
		ip = rawIP
	}
	if ip != "" && net.ParseIP(ip) == nil {
		return nil, errors.New("invalid IP address: " + ip)
	}
	if containerPort == "" {
		return nil, errors.Errorf("no port specified: %s<empty>", rawPort)
	}

	startPort, endPort, err := parsePortRange(containerPort)
	if err != nil {
		return nil, errors.New("invalid containerPort: " + containerPort)
	}

	var startHostPort, endHostPort uint64
	if hostPort != "" {
		startHostPort, endHostPort, err = parsePortRange(hostPort)
		if err != nil {
			return nil, errors.New("invalid hostPort: " + hostPort)
		}
		if (endPort - startPort) != (endHostPort - startHostPort) {
			// Allow host port range iff containerPort is not a range.
			// In this case, use the host port range as the dynamic
			// host port range to allocate into.
			if endPort != startPort {
				return nil, errors.Errorf("invalid ranges specified for container and host Ports: %s and %s", containerPort, hostPort)
			}
		}
	}

	count := endPort - startPort + 1
	ports := make([]nat.PortMapping, 0, count)

	for i := range count {
		cPort := nat.Port(strconv.FormatUint(startPort+i, 10) + "/" + proto)
		hPort := ""
		if hostPort != "" {
			hPort = strconv.FormatUint(startHostPort+i, 10)
			// Set hostPort to a range only if there is a single container port
			// and a dynamic host port.
			if count == 1 && startHostPort != endHostPort {
				hPort += "-" + strconv.FormatUint(endHostPort, 10)
			}
		}
		ports = append(ports, nat.PortMapping{
			Port:    cPort,
			Binding: nat.PortBinding{HostIP: ip, HostPort: hPort},
		})
	}
	return ports, nil
}

// parsePortRange parses and validates the specified string as a port-range (8000-9000)
func parsePortRange(ports string) (uint64, uint64, error) {
	if ports == "" {
		return 0, 0, errors.New("empty string specified for ports")
	}
	if !strings.Contains(ports, "-") {
		start, err := strconv.ParseUint(ports, 10, 16)
		end := start
		return start, end, err
	}

	parts := strings.Split(ports, "-")
	if len(parts) != 2 {
		return 0, 0, errors.Errorf("invalid port range format: %s", ports)
	}
	start, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, 0, err
	}
	end, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, errors.New("invalid range specified for port: " + ports)
	}
	return start, end, nil
}

package sshutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const defaultPort = 22

// keyScanTimeout bounds connecting to the server and the handshake that follows.
// The scan is best effort, so it must not hold up the caller for long.
const keyScanTimeout = 10 * time.Second

var errCallbackDone = errors.New("callback failed on purpose")

// knownHostsServerID formats a host identifier for a known_hosts entry. A
// non-standard port must be rendered as "[host]:port" so that ssh matches the
// entry when connecting (see the SSH_KNOWN_HOSTS format in sshd(8)); the
// default port is rendered as a bare hostname.
func knownHostsServerID(hostname, port string) string {
	if port == "" || port == strconv.Itoa(defaultPort) {
		return hostname
	}
	return fmt.Sprintf("[%s]:%s", hostname, port)
}

// addDefaultPort appends a default port if hostport doesn't contain one
func addDefaultPort(hostport string, defaultPort int) string {
	_, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return hostport
	}
	// Strip brackets from an IPv6 host before JoinHostPort adds its own.
	// SplitHostPort cannot extract the host here because it requires a port.
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		hostport = hostport[1 : len(hostport)-1]
	}
	hostport = net.JoinHostPort(hostport, strconv.Itoa(defaultPort))
	return hostport
}

// SSHKeyScan scans a ssh server for the hostkey; server should be in the form hostname, or hostname:port
func SSHKeyScan(server string) (string, error) {
	var key string
	KeyScanCallback := func(hostport string, remote net.Addr, pubKey ssh.PublicKey) error {
		hostname, port, err := net.SplitHostPort(hostport)
		if err != nil {
			return err
		}
		serverID := knownHostsServerID(hostname, port)
		key = strings.TrimSpace(fmt.Sprintf("%s %s", serverID, string(ssh.MarshalAuthorizedKey(pubKey))))
		return errCallbackDone
	}
	config := &ssh.ClientConfig{
		HostKeyCallback: KeyScanCallback,
	}

	server = addDefaultPort(server, defaultPort)
	dialer := net.Dialer{Timeout: keyScanTimeout}
	conn, err := dialer.DialContext(context.Background(), "tcp", server)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// The deadline also bounds the handshake, which ssh.ClientConfig.Timeout
	// does not cover.
	if err := conn.SetDeadline(time.Now().Add(keyScanTimeout)); err != nil {
		return "", err
	}

	sshConn, _, _, err := ssh.NewClientConn(conn, server, config)
	if key != "" {
		// as long as we get the key, the function worked
		err = nil
	}
	if sshConn != nil {
		_ = sshConn.Close()
	}
	return key, err
}

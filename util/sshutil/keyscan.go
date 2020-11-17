package sshutil

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

const defaultPort = 22

var ErrMalformedServer = fmt.Errorf("invalid server, must be of the form hostname, or hostname:port")

var errCallbackDone = fmt.Errorf("callback failed on purpose")

// SshKeyScan scans a ssh server for the hostkey; server should be in the form hostname, or hostname:port
func SSHKeyScan(server string) (string, error) {
	port := defaultPort
	parts := strings.Split(server, ":")
	if len(parts) == 2 {
		var err error
		server = parts[0]
		port, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", ErrMalformedServer
		}
	} else if len(parts) > 2 {
		return "", ErrMalformedServer
	}

	var key string
	KeyScanCallback := func(hostname string, remote net.Addr, pubKey ssh.PublicKey) error {
		hostname = strings.TrimSuffix(hostname, fmt.Sprintf(":%d", port))
		key = strings.TrimSpace(fmt.Sprintf("%s %s", hostname, string(ssh.MarshalAuthorizedKey(pubKey))))
		return errCallbackDone
	}
	config := &ssh.ClientConfig{
		HostKeyCallback: KeyScanCallback,
	}

	conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", server, port), config)
	if key != "" {
		// as long as we get the key, the function worked
		err = nil
	}
	if conn != nil {
		conn.Close()
	}
	return key, err
}

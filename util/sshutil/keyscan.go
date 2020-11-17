package sshutil

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

const defaultPort = 22

var ErrMalformedServer = fmt.Errorf("invalid server, must be of the form hostname, or hostname:port")

var errCallbackDone = fmt.Errorf("callback failed on purpose")

// SshKeyScan scans a ssh server for the hostkey; server should be in the form hostname, or hostname:port
func SSHKeyScan(server string) (string, error) {
	var key string
	KeyScanCallback := func(hostname string, remote net.Addr, pubKey ssh.PublicKey) error {
		key = strings.TrimSpace(fmt.Sprintf("%s %s", hostname[:len(hostname)-3], string(ssh.MarshalAuthorizedKey(pubKey))))
		return errCallbackDone
	}
	config := &ssh.ClientConfig{
		HostKeyCallback: KeyScanCallback,
	}

	var serverAndPort string
	parts := strings.Split(server, ":")
	if len(parts) == 1 {
		serverAndPort = fmt.Sprintf("%s:%d", server, defaultPort)
	} else if len(parts) == 2 {
		serverAndPort = server
	} else {
		return "", ErrMalformedServer
	}

	conn, err := ssh.Dial("tcp", serverAndPort, config)
	if key != "" {
		// as long as we get the key, the function worked
		err = nil
	}
	if conn != nil {
		conn.Close()
	}
	return key, err
}

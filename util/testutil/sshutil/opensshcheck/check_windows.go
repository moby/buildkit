// Package opensshcheck verifies agent authentication with Windows OpenSSH.
package opensshcheck

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

const (
	command = "buildkit-openssh-check"
	marker  = "authenticated with Windows OpenSSH"
)

// Config defines a Windows OpenSSH authentication check.
type Config struct {
	SSHPath  string
	Expected string
	Output   string
}

// Run authenticates through the configured agent and writes a success marker.
func Run(ctx context.Context, cfg Config) error {
	if cfg.SSHPath == "" || cfg.Expected == "" || cfg.Output == "" {
		return errors.New("ssh path, expected key, and output are required")
	}
	expectedBlob, err := base64.StdEncoding.DecodeString(cfg.Expected)
	if err != nil {
		return errors.Wrap(err, "decoding expected public key")
	}
	expected, err := ssh.ParsePublicKey(expectedBlob)
	if err != nil {
		return errors.Wrap(err, "parsing expected public key")
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.Wrap(err, "generating SSH host key")
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		return errors.Wrap(err, "creating SSH host signer")
	}
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), expected.Marshal()) {
				return nil, errors.New("unexpected SSH public key")
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)

	listenerConfig := net.ListenConfig{}
	listener, err := listenerConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return errors.Wrap(err, "listening for SSH client")
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- serve(listener, serverConfig)
	}()

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	args := []string{
		"-F", "NUL",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=NUL",
		"-o", "LogLevel=ERROR",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PreferredAuthentications=publickey",
		"-p", port,
		"buildkit@127.0.0.1",
		command,
	}
	client := exec.CommandContext(ctx, cfg.SSHPath, args...) //nolint:gosec // Test harness supplies the OpenSSH executable; no shell is used.
	out, clientErr := client.CombinedOutput()
	if clientErr != nil {
		_ = listener.Close()
		<-serverErr
		return errors.Wrapf(clientErr, "running Windows OpenSSH client: %s", out)
	}
	if err := <-serverErr; err != nil {
		return err
	}
	if !strings.Contains(strings.ReplaceAll(string(out), "\r\n", "\n"), marker+"\n") {
		return errors.Errorf("unexpected Windows OpenSSH output %q", out)
	}
	if err := os.WriteFile(cfg.Output, []byte(marker+"\n"), 0600); err != nil {
		return errors.Wrap(err, "writing Windows OpenSSH result")
	}
	return nil
}

func serve(listener net.Listener, config *ssh.ServerConfig) error {
	conn, err := listener.Accept()
	if err != nil {
		return errors.Wrap(err, "accepting SSH client")
	}
	defer conn.Close()
	_ = listener.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return errors.Wrap(err, "setting SSH server deadline")
	}
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return errors.Wrap(err, "performing SSH server handshake")
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session channel required")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return errors.Wrap(err, "accepting SSH session")
		}
		for request := range channelRequests {
			if request.Type != "exec" {
				if err := request.Reply(false, nil); err != nil {
					return errors.Wrap(err, "rejecting SSH session request")
				}
				continue
			}
			var payload struct {
				Command string
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
				return errors.Wrap(err, "decoding SSH command")
			}
			if payload.Command != command {
				return errors.Errorf("unexpected SSH command %q", payload.Command)
			}
			if err := request.Reply(true, nil); err != nil {
				return errors.Wrap(err, "accepting SSH command")
			}
			if _, err := io.WriteString(channel, marker+"\n"); err != nil {
				return errors.Wrap(err, "writing SSH command output")
			}
			if _, err := channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0})); err != nil {
				return errors.Wrap(err, "sending SSH exit status")
			}
			if err := channel.Close(); err != nil {
				return errors.Wrap(err, "closing SSH session")
			}
			_ = sshConn.Wait()
			return nil
		}
	}
	return errors.New("Windows OpenSSH client did not execute the test command")
}

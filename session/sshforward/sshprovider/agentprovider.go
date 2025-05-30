package sshprovider

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// AgentConfig is the config for a single exposed SSH agent
type AgentConfig struct {
	ID    string
	Paths []string
}

func (conf AgentConfig) ToRaw() (RawConfig, error) {
	if len(conf.Paths) == 0 || len(conf.Paths) == 1 && conf.Paths[0] == "" {
		conf.Paths = []string{os.Getenv("SSH_AUTH_SOCK")}
	}

	if conf.Paths[0] == "" {
		p, err := getFallbackAgentPath()
		if err != nil {
			return RawConfig{}, errors.Wrap(err, "invalid empty ssh agent socket")
		}
		conf.Paths[0] = p
	}

	src, err := toAgentSource(conf.Paths)
	if err != nil {
		return RawConfig{}, errors.Wrapf(err, "failed to convert agent config for ID: %q", conf.ID)
	}

	return RawConfig{
		ID:     conf.ID,
		Dialer: src.RawDialer,
	}, nil
}

// NewSSHAgentProvider creates a session provider that allows access to ssh agent
func NewSSHAgentProvider(confs []AgentConfig) (session.Attachable, error) {
	converted := make([]RawConfig, 0, len(confs))
	for _, conf := range confs {
		raw, err := conf.ToRaw()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to convert agent config %v", conf)
		}
		converted = append(converted, raw)
	}
	return newRawProvider(converted)
}

type source struct {
	agent  agent.Agent
	socket *socketDialer
}

type socketDialer struct {
	path   string
	dialer func(string) (net.Conn, error)
}

func (s source) RawDialer(ctx context.Context) (net.Conn, error) {
	var a agent.Agent

	if s.socket != nil {
		conn, err := s.socket.Dial()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to connect to %s", s.socket)
		}

		a = &readOnlyAgent{agent.NewClient(conn)}
		defer conn.Close()
	} else {
		a = s.agent
	}

	c1, c2 := net.Pipe()
	go func() {
		agent.ServeAgent(a, c1)
		c1.Close()
	}()

	return c2, nil
}

func (s socketDialer) Dial() (net.Conn, error) {
	return s.dialer(s.path)
}

func (s socketDialer) String() string {
	return s.path
}

func toAgentSource(paths []string) (source, error) {
	var keys bool
	var socket *socketDialer
	a := agent.NewKeyring()
	for _, p := range paths {
		if socket != nil {
			return source{}, errors.New("only single socket allowed")
		}

		if parsed := getWindowsPipeDialer(p); parsed != nil {
			socket = parsed
			continue
		}

		fi, err := os.Stat(p)
		if err != nil {
			return source{}, errors.WithStack(err)
		}
		if fi.Mode()&os.ModeSocket > 0 {
			socket = &socketDialer{path: p, dialer: unixSocketDialer}
			continue
		}

		f, err := os.Open(p)
		if err != nil {
			return source{}, errors.Wrapf(err, "failed to open %s", p)
		}
		dt, err := io.ReadAll(&io.LimitedReader{R: f, N: 100 * 1024})
		_ = f.Close()
		if err != nil {
			return source{}, errors.Wrapf(err, "failed to read %s", p)
		}

		k, err := ssh.ParseRawPrivateKey(dt)
		if err != nil {
			// On Windows, os.ModeSocket isn't appropriately set on the file mode.
			// https://github.com/golang/go/issues/33357
			// If parsing the file fails, check to see if it kind of looks like socket-shaped.
			if runtime.GOOS == "windows" && strings.Contains(string(dt), "socket") {
				if keys {
					return source{}, errors.Errorf("invalid combination of keys and sockets")
				}
				socket = &socketDialer{path: p, dialer: unixSocketDialer}
				continue
			}

			return source{}, errors.Wrapf(err, "failed to parse %s", p) // TODO: prompt passphrase?
		}
		if err := a.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			return source{}, errors.Wrapf(err, "failed to add %s to agent", p)
		}

		keys = true
	}

	if socket != nil {
		if keys {
			return source{}, errors.Errorf("invalid combination of keys and sockets")
		}
		return source{socket: socket}, nil
	}

	return source{agent: a}, nil
}

func unixSocketDialer(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 2*time.Second)
}

type readOnlyAgent struct {
	agent.ExtendedAgent
}

func (a *readOnlyAgent) Add(_ agent.AddedKey) error {
	return errors.Errorf("adding new keys not allowed by buildkit")
}

func (a *readOnlyAgent) Remove(_ ssh.PublicKey) error {
	return errors.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) RemoveAll() error {
	return errors.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) Lock(_ []byte) error {
	return errors.Errorf("locking agent not allowed by buildkit")
}

func (a *readOnlyAgent) Extension(_ string, _ []byte) ([]byte, error) {
	return nil, errors.Errorf("extensions not allowed by buildkit")
}

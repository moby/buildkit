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
	"github.com/moby/buildkit/session/sshforward"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// AgentConfig is the config for a single exposed SSH agent
type AgentConfig struct {
	ID    string
	Paths []string
	Raw   bool
}

func (conf AgentConfig) toDialer() (dialerFn, error) {
	if len(conf.Paths) != 1 && conf.Raw {
		return nil, errors.New("raw mode must supply exactly one path")
	}

	if len(conf.Paths) == 0 || len(conf.Paths) == 1 && conf.Paths[0] == "" {
		conf.Paths = []string{os.Getenv("SSH_AUTH_SOCK")}
	}

	if conf.Paths[0] == "" {
		p, err := getFallbackAgentPath()
		if err != nil {
			return nil, errors.Wrap(err, "invalid empty ssh agent socket")
		}
		conf.Paths[0] = p
	}

	dialer, err := toDialer(conf.Paths, conf.Raw)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to convert agent config for ID: %q", conf.ID)
	}

	return dialer, nil
}

// NewSSHAgentProvider creates a session provider that allows access to ssh agent
func NewSSHAgentProvider(confs []AgentConfig) (session.Attachable, error) {
	m := make(map[string]dialerFn, len(confs))
	for _, conf := range confs {
		if conf.ID == "" {
			conf.ID = sshforward.DefaultID
		}
		if _, ok := m[conf.ID]; ok {
			return nil, errors.Errorf("duplicate agent ID %q", conf.ID)
		}

		dialer, err := conf.toDialer()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to convert agent config %v", conf)
		}
		m[conf.ID] = dialer
	}
	return &socketProvider{
		m: m,
	}, nil
}

type source struct {
	agent  agent.Agent
	socket *socketDialer
}

type socketDialer struct {
	path   string
	dialer func(string) (net.Conn, error)
}

func (s source) agentDialer(ctx context.Context) (net.Conn, error) {
	var a agent.Agent

	var agentConn net.Conn
	if s.socket != nil {
		conn, err := s.socket.Dial(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to connect to %s", s.socket)
		}

		agentConn = conn
		a = &readOnlyAgent{agent.NewClient(conn)}
	} else {
		a = s.agent
	}

	c1, c2 := net.Pipe()
	go func() {
		agent.ServeAgent(a, c1)
		c1.Close()
		if agentConn != nil {
			agentConn.Close()
		}
	}()

	return c2, nil
}

func (s socketDialer) Dial(ctx context.Context) (net.Conn, error) {
	return s.dialer(s.path)
}

func (s socketDialer) String() string {
	return s.path
}

func toDialer(paths []string, raw bool) (func(context.Context) (net.Conn, error), error) {
	var keys bool
	var socket *socketDialer
	a := agent.NewKeyring()
	for _, p := range paths {
		if socket != nil {
			return nil, errors.New("only single socket allowed")
		}

		if parsed := getWindowsPipeDialer(p); parsed != nil {
			socket = parsed
			continue
		}

		fi, err := os.Stat(p)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if fi.Mode()&os.ModeSocket > 0 {
			socket = &socketDialer{path: p, dialer: unixSocketDialer}
			continue
		}
		if raw {
			return nil, errors.Errorf("raw mode only supported with socket paths")
		}

		f, err := os.Open(p)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to open %s", p)
		}
		dt, err := io.ReadAll(&io.LimitedReader{R: f, N: 100 * 1024})
		_ = f.Close()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read %s", p)
		}

		k, err := ssh.ParseRawPrivateKey(dt)
		if err != nil {
			// On Windows, os.ModeSocket isn't appropriately set on the file mode.
			// https://github.com/golang/go/issues/33357
			// If parsing the file fails, check to see if it kind of looks like socket-shaped.
			if runtime.GOOS == "windows" && strings.Contains(string(dt), "socket") {
				if keys {
					return nil, errors.Errorf("invalid combination of keys and sockets")
				}
				socket = &socketDialer{path: p, dialer: unixSocketDialer}
				continue
			}

			return nil, errors.Wrapf(err, "failed to parse %s", p) // TODO: prompt passphrase?
		}
		if err := a.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			return nil, errors.Wrapf(err, "failed to add %s to agent", p)
		}

		keys = true
	}

	if socket != nil {
		if keys {
			return nil, errors.Errorf("invalid combination of keys and sockets")
		}
		if raw {
			return func(ctx context.Context) (net.Conn, error) {
				return socket.Dial(ctx)
			}, nil
		}
		return source{socket: socket}.agentDialer, nil
	}

	if raw {
		return nil, errors.New("raw mode must supply exactly one socket path")
	}

	return source{agent: a}.agentDialer, nil
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

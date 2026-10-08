// Package probe exercises the SSH protocol over Windows named pipes.
package probe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"os"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Config struct {
	Endpoint string
	Expected string
	Absent   bool
	Mutate   bool
	Cycles   int
	Defaults bool
}

type Report struct {
	Keys              []string `json:"keys"`
	Endpoint          string   `json:"endpoint"`
	AuthSock          string   `json:"authSock"`
	Absent            bool     `json:"absent"`
	AddRejected       bool     `json:"addRejected"`
	RemoveAllRejected bool     `json:"removeAllRejected"`
	Connections       int      `json:"connections"`
}

func CheckIdentity(keys []*agent.Key, expected string) error {
	blob, err := base64.StdEncoding.DecodeString(expected)
	if err != nil {
		return errors.Wrap(err, "decoding expected public key")
	}
	if _, err := ssh.ParsePublicKey(blob); err != nil {
		return errors.Wrap(err, "parsing expected public key")
	}
	if len(keys) != 1 || base64.StdEncoding.EncodeToString(keys[0].Blob) != expected {
		return errors.Errorf("expected exactly public key %s, got %v", expected, keys)
	}
	return nil
}

func Run(ctx context.Context, cfg Config) (Report, error) {
	r := Report{AuthSock: os.Getenv("SSH_AUTH_SOCK")}
	r.Endpoint = cfg.Endpoint
	if r.Endpoint == "" {
		r.Endpoint = defaultEndpoint()
	}
	if cfg.Defaults {
		if err := checkDefaults(r); err != nil {
			return r, err
		}
	}
	if cfg.Absent {
		c, err := Dial(ctx, r.Endpoint)
		if err == nil {
			_ = c.Close()
			return r, errors.New("optional SSH endpoint unexpectedly exists")
		}
		if !isAbsent(err) {
			return r, errors.Wrap(err, "checking absent SSH endpoint")
		}
		r.Absent = true
		return r, nil
	}
	if cfg.Cycles < 1 {
		return r, errors.New("connections must be positive")
	}
	for range cfg.Cycles {
		if err := exchange(ctx, cfg, &r); err != nil {
			return r, err
		}
		r.Connections++
	}
	return r, nil
}

func exchange(ctx context.Context, cfg Config, r *Report) error {
	ready, cancel := context.WithTimeoutCause(ctx, 5*time.Second, errors.New("SSH endpoint readiness timed out"))
	defer cancel()
	var c net.Conn
	var err error
	for {
		c, err = Dial(ready, r.Endpoint)
		if err == nil {
			break
		}
		if !isAbsent(err) {
			return errors.Wrap(err, "dialing SSH endpoint")
		}
		select {
		case <-ready.Done():
			return errors.Wrap(context.Cause(ready), "dialing SSH endpoint")
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return errors.Wrap(err, "setting SSH protocol deadline")
	}
	// Hide Close to keep the agent client's background reader from consuming
	// the raw mutation responses below. This function owns the connection.
	a := agent.NewClient(struct{ io.ReadWriter }{c})
	keys, err := a.List()
	if err != nil {
		return errors.Wrap(err, "listing SSH keys")
	}
	if err := CheckIdentity(keys, cfg.Expected); err != nil {
		return err
	}
	r.Keys = []string{base64.StdEncoding.EncodeToString(keys[0].Blob)}
	if cfg.Mutate {
		public, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		request := ssh.Marshal(struct {
			Type    string `sshtype:"17"`
			Public  []byte
			Private []byte
			Comment string
		}{
			Type: ssh.KeyAlgoED25519, Public: public, Private: key,
		})
		if err := checkRejection(c, request); err != nil {
			return errors.Wrap(err, "adding key")
		}
		r.AddRejected = true
		if err := checkRejection(c, []byte{19}); err != nil { // SSH_AGENTC_REMOVE_ALL_IDENTITIES
			return errors.Wrap(err, "removing all keys")
		}
		r.RemoveAllRejected = true
		keys, err = a.List()
		if err != nil {
			return errors.Wrap(err, "listing keys after rejected mutations")
		}
		return CheckIdentity(keys, cfg.Expected)
	}
	return nil
}

func checkRejection(conn io.ReadWriter, request []byte) error {
	// The agent client collapses different response types into "agent: failure".
	// Check the wire response instead: SSH_AGENT_FAILURE is exactly one byte (5).
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(request)))
	frame = append(frame, request...)
	if n, err := conn.Write(frame); err != nil {
		return errors.Wrap(err, "writing SSH mutation request")
	} else if n != len(frame) {
		return errors.WithStack(io.ErrShortWrite)
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return errors.Wrap(err, "reading SSH mutation response length")
	}
	if size := binary.BigEndian.Uint32(header[:]); size != 1 {
		return errors.Errorf("expected one-byte SSH_AGENT_FAILURE, got response length %d", size)
	}
	var response [1]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return errors.Wrap(err, "reading SSH mutation response")
	}
	if response[0] != 5 {
		return errors.Errorf("expected SSH_AGENT_FAILURE (5), got response type %d", response[0])
	}
	return nil
}

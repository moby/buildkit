package noise

import (
	"bufio"
	"context"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type secureSession struct {
	initiator bool

	localID   peer.ID
	localKey  crypto.PrivKey
	remoteID  peer.ID
	remoteKey crypto.PubKey

	readLock  sync.Mutex
	writeLock sync.Mutex

	insecureConn   net.Conn
	insecureReader *bufio.Reader // to cushion io read syscalls
	// we don't buffer writes to avoid introducing latency; optimisation possible. // TODO revisit

	qseek int     // queued bytes seek value.
	qbuf  []byte  // queued bytes buffer.
	rlen  [2]byte // work buffer to read in the incoming message length.

	enc *noise.CipherState
	dec *noise.CipherState

	// noise prologue
	prologue []byte
}

// newSecureSession creates a Noise session over the given insecureConn Conn, using
// the libp2p identity keypair from the given Transport.
func newSecureSession(tpt *Transport, ctx context.Context, insecure net.Conn, remote peer.ID, prologue []byte, initiator bool) (*secureSession, error) {
	s := &secureSession{
		insecureConn:   insecure,
		insecureReader: bufio.NewReader(insecure),
		initiator:      initiator,
		localID:        tpt.localID,
		localKey:       tpt.privateKey,
		remoteID:       remote,
		prologue:       prologue,
	}

	// the go-routine we create to run the handshake will
	// write the result of the handshake to the respCh.
	respCh := make(chan error, 1)
	go func() {
		respCh <- s.runHandshake(ctx)
	}()

	select {
	case err := <-respCh:
		if err != nil {
			_ = s.insecureConn.Close()
		}
		return s, err

	case <-ctx.Done():
		// If the context has been cancelled, we close the underlying connection.
		// We then wait for the handshake to return because of the first error it encounters
		// so we don't return without cleaning up the go-routine.
		_ = s.insecureConn.Close()
		<-respCh
		return nil, ctx.Err()
	}
}

func (s *secureSession) LocalAddr() net.Addr {
	return s.insecureConn.LocalAddr()
}

func (s *secureSession) LocalPeer() peer.ID {
	return s.localID
}

func (s *secureSession) LocalPrivateKey() crypto.PrivKey {
	return s.localKey
}

func (s *secureSession) LocalPublicKey() crypto.PubKey {
	return s.localKey.GetPublic()
}

func (s *secureSession) RemoteAddr() net.Addr {
	return s.insecureConn.RemoteAddr()
}

func (s *secureSession) RemotePeer() peer.ID {
	return s.remoteID
}

func (s *secureSession) RemotePublicKey() crypto.PubKey {
	return s.remoteKey
}

func (s *secureSession) SetDeadline(t time.Time) error {
	return s.insecureConn.SetDeadline(t)
}

func (s *secureSession) SetReadDeadline(t time.Time) error {
	return s.insecureConn.SetReadDeadline(t)
}

func (s *secureSession) SetWriteDeadline(t time.Time) error {
	return s.insecureConn.SetWriteDeadline(t)
}

func (s *secureSession) Close() error {
	return s.insecureConn.Close()
}

// Package insecure provides an insecure, unencrypted implementation of the the SecureConn and SecureTransport interfaces.
//
// Recommended only for testing and other non-production usage.
package insecure

import (
	"context"
	"fmt"
	"io"
	"net"

	ci "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	pb "github.com/libp2p/go-libp2p/core/sec/insecure/pb"

	"github.com/libp2p/go-msgio"
)

// ID is the multistream-select protocol ID that should be used when identifying
// this security transport.
const ID = "/plaintext/2.0.0"

// Transport is a no-op stream security transport. It provides no
// security and simply mocks the security methods. Identity methods
// return the local peer's ID and private key, and whatever the remote
// peer presents as their ID and public key.
// No authentication of the remote identity is performed.
type Transport struct {
	id  peer.ID
	key ci.PrivKey
}

// NewWithIdentity constructs a new insecure transport. The provided private key
// is stored and returned from LocalPrivateKey to satisfy the
// SecureTransport interface, and the public key is sent to
// remote peers. No security is provided.
func NewWithIdentity(id peer.ID, key ci.PrivKey) *Transport {
	return &Transport{
		id:  id,
		key: key,
	}
}

// LocalPeer returns the transport's local peer ID.
func (t *Transport) LocalPeer() peer.ID {
	return t.id
}

// LocalPrivateKey returns the local private key.
// This key is used only for identity generation and provides no security.
func (t *Transport) LocalPrivateKey() ci.PrivKey {
	return t.key
}

// SecureInbound *pretends to secure* an inbound connection to the given peer.
// It sends the local peer's ID and public key, and receives the same from the remote peer.
// No validation is performed as to the authenticity or ownership of the provided public key,
// and the key exchange provides no security.
//
// SecureInbound may fail if the remote peer sends an ID and public key that are inconsistent
// with each other, or if a network error occurs during the ID exchange.
func (t *Transport) SecureInbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	conn := &Conn{
		Conn:         insecure,
		local:        t.id,
		localPrivKey: t.key,
	}

	err := conn.runHandshakeSync()
	if err != nil {
		return nil, err
	}

	if t.key != nil && p != "" && p != conn.remote {
		return nil, fmt.Errorf("remote peer sent unexpected peer ID. expected=%s received=%s", p, conn.remote)
	}

	return conn, nil
}

// SecureOutbound *pretends to secure* an outbound connection to the given peer.
// It sends the local peer's ID and public key, and receives the same from the remote peer.
// No validation is performed as to the authenticity or ownership of the provided public key,
// and the key exchange provides no security.
//
// SecureOutbound may fail if the remote peer sends an ID and public key that are inconsistent
// with each other, or if the ID sent by the remote peer does not match the one dialed. It may
// also fail if a network error occurs during the ID exchange.
func (t *Transport) SecureOutbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	conn := &Conn{
		Conn:         insecure,
		local:        t.id,
		localPrivKey: t.key,
	}

	err := conn.runHandshakeSync()
	if err != nil {
		return nil, err
	}

	if t.key != nil && p != conn.remote {
		return nil, fmt.Errorf("remote peer sent unexpected peer ID. expected=%s received=%s",
			p, conn.remote)
	}

	return conn, nil
}

// Conn is the connection type returned by the insecure transport.
type Conn struct {
	net.Conn

	local  peer.ID
	remote peer.ID

	localPrivKey ci.PrivKey
	remotePubKey ci.PubKey
}

func makeExchangeMessage(pubkey ci.PubKey) (*pb.Exchange, error) {
	keyMsg, err := ci.PublicKeyToProto(pubkey)
	if err != nil {
		return nil, err
	}
	id, err := peer.IDFromPublicKey(pubkey)
	if err != nil {
		return nil, err
	}

	return &pb.Exchange{
		Id:     []byte(id),
		Pubkey: keyMsg,
	}, nil
}

func (ic *Conn) runHandshakeSync() error {
	// If we were initialized without keys, behave as in plaintext/1.0.0 (do nothing)
	if ic.localPrivKey == nil {
		return nil
	}

	// Generate an Exchange message
	msg, err := makeExchangeMessage(ic.localPrivKey.GetPublic())
	if err != nil {
		return err
	}

	// Send our Exchange and read theirs
	remoteMsg, err := readWriteMsg(ic.Conn, msg)
	if err != nil {
		return err
	}

	// Pull remote ID and public key from message
	remotePubkey, err := ci.PublicKeyFromProto(remoteMsg.Pubkey)
	if err != nil {
		return err
	}

	remoteID, err := peer.IDFromBytes(remoteMsg.Id)
	if err != nil {
		return err
	}

	// Validate that ID matches public key
	if !remoteID.MatchesPublicKey(remotePubkey) {
		calculatedID, _ := peer.IDFromPublicKey(remotePubkey)
		return fmt.Errorf("remote peer id does not match public key. id=%s calculated_id=%s",
			remoteID, calculatedID)
	}

	// Add remote ID and key to conn state
	ic.remotePubKey = remotePubkey
	ic.remote = remoteID
	return nil
}

// read and write a message at the same time.
func readWriteMsg(rw io.ReadWriter, out *pb.Exchange) (*pb.Exchange, error) {
	const maxMessageSize = 1 << 16

	outBytes, err := out.Marshal()
	if err != nil {
		return nil, err
	}
	wresult := make(chan error)
	go func() {
		w := msgio.NewVarintWriter(rw)
		wresult <- w.WriteMsg(outBytes)
	}()

	r := msgio.NewVarintReaderSize(rw, maxMessageSize)
	msg, err1 := r.ReadMsg()

	// Always wait for the read to finish.
	err2 := <-wresult

	if err1 != nil {
		return nil, err1
	}
	if err2 != nil {
		r.ReleaseMsg(msg)
		return nil, err2
	}
	inMsg := new(pb.Exchange)
	err = inMsg.Unmarshal(msg)
	return inMsg, err
}

// LocalPeer returns the local peer ID.
func (ic *Conn) LocalPeer() peer.ID {
	return ic.local
}

// RemotePeer returns the remote peer ID if we initiated the dial. Otherwise, it
// returns "" (because this connection isn't actually secure).
func (ic *Conn) RemotePeer() peer.ID {
	return ic.remote
}

// RemotePublicKey returns whatever public key was given by the remote peer.
// Note that no verification of ownership is done, as this connection is not secure.
func (ic *Conn) RemotePublicKey() ci.PubKey {
	return ic.remotePubKey
}

// LocalPrivateKey returns the private key for the local peer.
func (ic *Conn) LocalPrivateKey() ci.PrivKey {
	return ic.localPrivKey
}

var _ sec.SecureTransport = (*Transport)(nil)
var _ sec.SecureConn = (*Conn)(nil)

// Deprecated: This package has moved into go-libp2p as a sub-package: github.com/libp2p/go-libp2p/core/peer.
//
// Package peer implements an object used to represent peers in the libp2p network.
package peer

import (
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/ipfs/go-cid"
	ic "github.com/libp2p/go-libp2p-core/crypto"
)

var (
	// ErrEmptyPeerID is an error for empty peer ID.
	// Deprecated: use github.com/libp2p/go-libp2p/core/peer.ErrEmptyPeerID instead
	ErrEmptyPeerID = peer.ErrEmptyPeerID
	// ErrNoPublicKey is an error for peer IDs that don't embed public keys
	// Deprecated: use github.com/libp2p/go-libp2p/core/peer.ErrNoPublicKey instead
	ErrNoPublicKey = peer.ErrNoPublicKey
)

// AdvancedEnableInlining enables automatically inlining keys shorter than
// 42 bytes into the peer ID (using the "identity" multihash function).
//
// WARNING: This flag will likely be set to false in the future and eventually
// be removed in favor of using a hash function specified by the key itself.
// See: https://github.com/libp2p/specs/issues/138
//
// DO NOT change this flag unless you know what you're doing.
//
// This currently defaults to true for backwards compatibility but will likely
// be set to false by default when an upgrade path is determined.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AdvancedEnableInlining instead
var AdvancedEnableInlining = peer.AdvancedEnableInlining

const maxInlineKeyLength = 42

// ID is a libp2p peer identity.
//
// Peer IDs are derived by hashing a peer's public key and encoding the
// hash output as a multihash. See IDFromPublicKey for details.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.ID instead
type ID = peer.ID

// IDFromBytes casts a byte slice to the ID type, and validates
// the value to make sure it is a multihash.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.IDFromBytes instead
func IDFromBytes(b []byte) (ID, error) {
	return peer.IDFromBytes(b)
}

// Decode accepts an encoded peer ID and returns the decoded ID if the input is
// valid.
//
// The encoded peer ID can either be a CID of a key or a raw multihash (identity
// or sha256-256).
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.Decode instead
func Decode(s string) (ID, error) {
	return peer.Decode(s)
}

// Encode encodes a peer ID as a string.
//
// At the moment, it base58 encodes the peer ID but, in the future, it will
// switch to encoding it as a CID by default.
//
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.Encode instead
func Encode(id ID) string {
	return peer.Encode(id)
}

// FromCid converts a CID to a peer ID, if possible.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.FromCid instead
func FromCid(c cid.Cid) (ID, error) {
	return peer.FromCid(c)
}

// ToCid encodes a peer ID as a CID of the public key.
//
// If the peer ID is invalid (e.g., empty), this will return the empty CID.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.ToCid instead
func ToCid(id ID) cid.Cid {
	return peer.ToCid(id)
}

// IDFromPublicKey returns the Peer ID corresponding to the public key pk.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.IDFromPublicKey instead
func IDFromPublicKey(pk ic.PubKey) (ID, error) {
	return peer.IDFromPublicKey(pk)
}

// IDFromPrivateKey returns the Peer ID corresponding to the secret key sk.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.IDFromPrivateKey instead
func IDFromPrivateKey(sk ic.PrivKey) (ID, error) {
	return peer.IDFromPrivateKey(sk)
}

// IDSlice for sorting peers.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.IDSlice instead
type IDSlice = peer.IDSlice

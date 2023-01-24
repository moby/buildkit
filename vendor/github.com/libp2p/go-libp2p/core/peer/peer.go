// Package peer implements an object used to represent peers in the libp2p network.
package peer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ipfs/go-cid"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	b58 "github.com/mr-tron/base58/base58"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
)

var (
	// ErrEmptyPeerID is an error for empty peer ID.
	ErrEmptyPeerID = errors.New("empty peer ID")
	// ErrNoPublicKey is an error for peer IDs that don't embed public keys
	ErrNoPublicKey = errors.New("public key is not embedded in peer ID")
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
var AdvancedEnableInlining = true

const maxInlineKeyLength = 42

// ID is a libp2p peer identity.
//
// Peer IDs are derived by hashing a peer's public key and encoding the
// hash output as a multihash. See IDFromPublicKey for details.
type ID string

// Pretty returns a base58-encoded string representation of the ID.
// Deprecated: use String() instead.
func (id ID) Pretty() string {
	return id.String()
}

// Loggable returns a pretty peer ID string in loggable JSON format.
func (id ID) Loggable() map[string]interface{} {
	return map[string]interface{}{
		"peerID": id.String(),
	}
}

func (id ID) String() string {
	return b58.Encode([]byte(id))
}

// ShortString prints out the peer ID.
//
// TODO(brian): ensure correctness at ID generation and
// enforce this by only exposing functions that generate
// IDs safely. Then any peer.ID type found in the
// codebase is known to be correct.
func (id ID) ShortString() string {
	pid := id.String()
	if len(pid) <= 10 {
		return fmt.Sprintf("<peer.ID %s>", pid)
	}
	return fmt.Sprintf("<peer.ID %s*%s>", pid[:2], pid[len(pid)-6:])
}

// MatchesPrivateKey tests whether this ID was derived from the secret key sk.
func (id ID) MatchesPrivateKey(sk ic.PrivKey) bool {
	return id.MatchesPublicKey(sk.GetPublic())
}

// MatchesPublicKey tests whether this ID was derived from the public key pk.
func (id ID) MatchesPublicKey(pk ic.PubKey) bool {
	oid, err := IDFromPublicKey(pk)
	if err != nil {
		return false
	}
	return oid == id
}

// ExtractPublicKey attempts to extract the public key from an ID.
//
// This method returns ErrNoPublicKey if the peer ID looks valid but it can't extract
// the public key.
func (id ID) ExtractPublicKey() (ic.PubKey, error) {
	decoded, err := mh.Decode([]byte(id))
	if err != nil {
		return nil, err
	}
	if decoded.Code != mh.IDENTITY {
		return nil, ErrNoPublicKey
	}
	pk, err := ic.UnmarshalPublicKey(decoded.Digest)
	if err != nil {
		return nil, err
	}
	return pk, nil
}

// Validate checks if ID is empty or not.
func (id ID) Validate() error {
	if id == ID("") {
		return ErrEmptyPeerID
	}

	return nil
}

// IDFromBytes casts a byte slice to the ID type, and validates
// the value to make sure it is a multihash.
func IDFromBytes(b []byte) (ID, error) {
	if _, err := mh.Cast(b); err != nil {
		return ID(""), err
	}
	return ID(b), nil
}

// Decode accepts an encoded peer ID and returns the decoded ID if the input is
// valid.
//
// The encoded peer ID can either be a CID of a key or a raw multihash (identity
// or sha256-256).
func Decode(s string) (ID, error) {
	if strings.HasPrefix(s, "Qm") || strings.HasPrefix(s, "1") {
		// base58 encoded sha256 or identity multihash
		m, err := mh.FromB58String(s)
		if err != nil {
			return "", fmt.Errorf("failed to parse peer ID: %s", err)
		}
		return ID(m), nil
	}

	c, err := cid.Decode(s)
	if err != nil {
		return "", fmt.Errorf("failed to parse peer ID: %s", err)
	}
	return FromCid(c)
}

// Encode encodes a peer ID as a string.
//
// At the moment, it base58 encodes the peer ID but, in the future, it will
// switch to encoding it as a CID by default.
//
// Deprecated: use id.String instead.
func Encode(id ID) string {
	return id.String()
}

// FromCid converts a CID to a peer ID, if possible.
func FromCid(c cid.Cid) (ID, error) {
	code := mc.Code(c.Type())
	if code != mc.Libp2pKey {
		return "", fmt.Errorf("can't convert CID of type %q to a peer ID", code)
	}
	return ID(c.Hash()), nil
}

// ToCid encodes a peer ID as a CID of the public key.
//
// If the peer ID is invalid (e.g., empty), this will return the empty CID.
func ToCid(id ID) cid.Cid {
	m, err := mh.Cast([]byte(id))
	if err != nil {
		return cid.Cid{}
	}
	return cid.NewCidV1(cid.Libp2pKey, m)
}

// IDFromPublicKey returns the Peer ID corresponding to the public key pk.
func IDFromPublicKey(pk ic.PubKey) (ID, error) {
	b, err := ic.MarshalPublicKey(pk)
	if err != nil {
		return "", err
	}
	var alg uint64 = mh.SHA2_256
	if AdvancedEnableInlining && len(b) <= maxInlineKeyLength {
		alg = mh.IDENTITY
	}
	hash, _ := mh.Sum(b, alg, -1)
	return ID(hash), nil
}

// IDFromPrivateKey returns the Peer ID corresponding to the secret key sk.
func IDFromPrivateKey(sk ic.PrivKey) (ID, error) {
	return IDFromPublicKey(sk.GetPublic())
}

// IDSlice for sorting peers.
type IDSlice []ID

func (es IDSlice) Len() int           { return len(es) }
func (es IDSlice) Swap(i, j int)      { es[i], es[j] = es[j], es[i] }
func (es IDSlice) Less(i, j int) bool { return string(es[i]) < string(es[j]) }

func (es IDSlice) String() string {
	peersStrings := make([]string, len(es))
	for i, id := range es {
		peersStrings[i] = id.String()
	}
	return strings.Join(peersStrings, ", ")
}

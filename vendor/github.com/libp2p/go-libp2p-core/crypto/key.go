// Deprecated: This package has moved into go-libp2p as a sub-package: github.com/libp2p/go-libp2p/core/crypto.
//
// Package crypto implements various cryptographic utilities used by libp2p.
// This includes a Public and Private key interface and key implementations
// for supported key algorithms.
package crypto

import (
	"errors"
	"io"

	"github.com/libp2p/go-libp2p/core/crypto"
	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
)

const (
	// RSA is an enum for the supported RSA key type
	RSA = iota
	// Ed25519 is an enum for the supported Ed25519 key type
	Ed25519
	// Secp256k1 is an enum for the supported Secp256k1 key type
	Secp256k1
	// ECDSA is an enum for the supported ECDSA key type
	ECDSA
)

var (
	// ErrBadKeyType is returned when a key is not supported
	ErrBadKeyType = errors.New("invalid or unsupported key type")
	// KeyTypes is a list of supported keys
	KeyTypes = []int{
		RSA,
		Ed25519,
		Secp256k1,
		ECDSA,
	}
)

// PubKeyUnmarshaller is a func that creates a PubKey from a given slice of bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PubKeyUnmarshaller instead
type PubKeyUnmarshaller = crypto.PubKeyUnmarshaller

// PrivKeyUnmarshaller is a func that creates a PrivKey from a given slice of bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PrivKeyUnmarshaller instead
type PrivKeyUnmarshaller = crypto.PrivKeyUnmarshaller

// PubKeyUnmarshallers is a map of unmarshallers by key type
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PubKeyUnmarshallers instead
var PubKeyUnmarshallers = crypto.PubKeyUnmarshallers

// PrivKeyUnmarshallers is a map of unmarshallers by key type
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PrivKeyUnmarshallers instead
var PrivKeyUnmarshallers = crypto.PrivKeyUnmarshallers

// Key represents a crypto key that can be compared to another key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.Key instead
type Key = crypto.Key

// PrivKey represents a private key that can be used to generate a public key and sign data
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PrivKey instead
type PrivKey = crypto.PrivKey

// PubKey is a public key that can be used to verifiy data signed with the corresponding private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PubKey instead
type PubKey = crypto.PubKey

// GenSharedKey generates the shared key from a given private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenSharedKey instead
type GenSharedKey = crypto.GenSharedKey

// GenerateKeyPair generates a private and public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateKeyPair instead
func GenerateKeyPair(typ, bits int) (PrivKey, PubKey, error) {
	return crypto.GenerateKeyPair(typ, bits)
}

// GenerateKeyPairWithReader returns a keypair of the given type and bitsize
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateKeyPairWithReader instead
func GenerateKeyPairWithReader(typ, bits int, src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateKeyPairWithReader(typ, bits, src)
}

// GenerateEKeyPair returns an ephemeral public key and returns a function that will compute
// the shared secret key.  Used in the identify module.
//
// Focuses only on ECDH now, but can be made more general in the future.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateEKeyPair instead
func GenerateEKeyPair(curveName string) ([]byte, GenSharedKey, error) {
	return crypto.GenerateEKeyPair(curveName)
}

// UnmarshalPublicKey converts a protobuf serialized public key into its
// representative object
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalPublicKey instead
func UnmarshalPublicKey(data []byte) (PubKey, error) {
	return crypto.UnmarshalPublicKey(data)
}

// PublicKeyFromProto converts an unserialized protobuf PublicKey message
// into its representative object.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PublicKeyFromProto instead
func PublicKeyFromProto(pmes *pb.PublicKey) (PubKey, error) {
	return crypto.PublicKeyFromProto(pmes)
}

// MarshalPublicKey converts a public key object into a protobuf serialized
// public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MarshalPublicKey instead
func MarshalPublicKey(k PubKey) ([]byte, error) {
	return crypto.MarshalPublicKey(k)
}

// PublicKeyToProto converts a public key object into an unserialized
// protobuf PublicKey message.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PublicKeyToProto instead
func PublicKeyToProto(k PubKey) (*pb.PublicKey, error) {
	return crypto.PublicKeyToProto(k)
}

// UnmarshalPrivateKey converts a protobuf serialized private key into its
// representative object
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalPrivateKey instead
func UnmarshalPrivateKey(data []byte) (PrivKey, error) {
	return crypto.UnmarshalPrivateKey(data)
}

// MarshalPrivateKey converts a key object into its protobuf serialized form.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MarshalPrivateKey instead
func MarshalPrivateKey(k PrivKey) ([]byte, error) {
	return crypto.MarshalPrivateKey(k)
}

// ConfigDecodeKey decodes from b64 (for config file) to a byte array that can be unmarshalled.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ConfigDecodeKey instead
func ConfigDecodeKey(b string) ([]byte, error) {
	return crypto.ConfigDecodeKey(b)
}

// ConfigEncodeKey encodes a marshalled key to b64 (for config file).
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ConfigEncodeKey instead
func ConfigEncodeKey(b []byte) string {
	return crypto.ConfigEncodeKey(b)
}

// KeyEqual checks whether two Keys are equivalent (have identical byte representations).
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.KeyEqual instead
func KeyEqual(k1, k2 Key) bool {
	return crypto.KeyEqual(k1, k2)
}

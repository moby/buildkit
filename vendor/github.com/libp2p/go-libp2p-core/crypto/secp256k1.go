package crypto

import (
	"io"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// Secp256k1PrivateKey is an Secp256k1 private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.Secp256k1PrivateKey instead
type Secp256k1PrivateKey = crypto.Secp256k1PrivateKey

// Secp256k1PublicKey is an Secp256k1 public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.Secp256k1PublicKey instead
type Secp256k1PublicKey = crypto.Secp256k1PublicKey

// GenerateSecp256k1Key generates a new Secp256k1 private and public key pair
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateSecp256k1Key instead
func GenerateSecp256k1Key(src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateSecp256k1Key(src)
}

// UnmarshalSecp256k1PrivateKey returns a private key from bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalSecp256k1PrivateKey instead
func UnmarshalSecp256k1PrivateKey(data []byte) (k PrivKey, err error) {
	return crypto.UnmarshalSecp256k1PrivateKey(data)
}

// UnmarshalSecp256k1PublicKey returns a public key from bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalSecp256k1PublicKey instead
func UnmarshalSecp256k1PublicKey(data []byte) (_k PubKey, err error) {
	return crypto.UnmarshalSecp256k1PublicKey(data)
}

package crypto

import (
	"io"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// Ed25519PrivateKey is an ed25519 private key.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.Ed25519PrivateKey instead
type Ed25519PrivateKey = crypto.Ed25519PrivateKey

// Ed25519PublicKey is an ed25519 public key.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.Ed25519PublicKey instead
type Ed25519PublicKey = crypto.Ed25519PublicKey

// GenerateEd25519Key generates a new ed25519 private and public key pair.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateEd25519Key instead
func GenerateEd25519Key(src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateEd25519Key(src)
}

// UnmarshalEd25519PublicKey returns a public key from input bytes.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalEd25519PublicKey instead
func UnmarshalEd25519PublicKey(data []byte) (PubKey, error) {
	return crypto.UnmarshalEd25519PublicKey(data)
}

// UnmarshalEd25519PrivateKey returns a private key from input bytes.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalEd25519PrivateKey instead
func UnmarshalEd25519PrivateKey(data []byte) (PrivKey, error) {
	return crypto.UnmarshalEd25519PrivateKey(data)
}

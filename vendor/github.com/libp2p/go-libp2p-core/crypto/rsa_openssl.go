//go:build openssl
// +build openssl

package crypto

import (
	"io"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// RsaPrivateKey is an rsa private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.RsaPrivateKey instead
type RsaPrivateKey = crypto.RsaPrivateKey

// RsaPublicKey is an rsa public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.RsaPublicKey instead
type RsaPublicKey = crypto.RsaPublicKey

// GenerateRSAKeyPair generates a new rsa private and public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateRSAKeyPair instead
func GenerateRSAKeyPair(bits int, r io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateRSAKeyPair(bits, r)
}

// UnmarshalRsaPrivateKey returns a private key from the input x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalRsaPrivateKey instead
func UnmarshalRsaPrivateKey(b []byte) (PrivKey, error) {
	return crypto.UnmarshalRsaPrivateKey(b)
}

// UnmarshalRsaPublicKey returns a public key from the input x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalRsaPublicKey instead
func UnmarshalRsaPublicKey(b []byte) (PubKey, error) {
	return crypto.UnmarshalRsaPublicKey(b)
}

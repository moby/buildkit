//go:build !openssl
// +build !openssl

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
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateRSAKeyPair
func GenerateRSAKeyPair(bits int, src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateRSAKeyPair(bits, src)
}

// UnmarshalRsaPrivateKey returns a private key from the input x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalRsaPrivateKey
func UnmarshalRsaPrivateKey(b []byte) (key PrivKey, err error) {
	return crypto.UnmarshalRsaPrivateKey(b)
}

// UnmarshalRsaPublicKey returns a public key from the input x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalRsaPublicKey
func UnmarshalRsaPublicKey(b []byte) (key PubKey, err error) {
	return crypto.UnmarshalRsaPublicKey(b)
}

//go:build !openssl
// +build !openssl

package crypto

import (
	stdcrypto "crypto"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// KeyPairFromStdKey wraps standard library (and secp256k1) private keys in libp2p/go-libp2p-core/crypto keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.KeyPairFromStdKey instead
func KeyPairFromStdKey(priv stdcrypto.PrivateKey) (PrivKey, PubKey, error) {
	return crypto.KeyPairFromStdKey(priv)
}

// PrivKeyToStdKey converts libp2p/go-libp2p-core/crypto private keys to standard library (and secp256k1) private keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PrivKeyToStdKey instead
func PrivKeyToStdKey(priv PrivKey) (stdcrypto.PrivateKey, error) {
	return crypto.PrivKeyToStdKey(priv)
}

// PubKeyToStdKey converts libp2p/go-libp2p-core/crypto private keys to standard library (and secp256k1) public keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PubKeyToStdKey instead
func PubKeyToStdKey(pub PubKey) (stdcrypto.PublicKey, error) {
	return crypto.PubKeyToStdKey(pub)
}

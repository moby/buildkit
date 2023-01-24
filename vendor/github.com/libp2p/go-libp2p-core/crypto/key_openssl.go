//go:build openssl
// +build openssl

package crypto

import (
	stdcrypto "crypto"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// KeyPairFromStdKey wraps standard library (and secp256k1) private keys in libp2p/go-libp2p-core/crypto keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.KeyPairFromStdKey instead
func KeyPairFromStdKey(priv stdcrypto.PrivateKey) (_priv PrivKey, _pub PubKey, err error) {
	return crypto.KeyPairFromStdKey(priv)
}

// PrivKeyToStdKey converts libp2p/go-libp2p-core/crypto private keys to standard library (and secp256k1) private keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PrivKeyToStdKey instead
func PrivKeyToStdKey(priv PrivKey) (_priv stdcrypto.PrivateKey, err error) {
	return crypto.PrivKeyToStdKey(priv)
}

// PubKeyToStdKey converts libp2p/go-libp2p-core/crypto private keys to standard library (and secp256k1) public keys
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.PubKeyToStdKey instead
func PubKeyToStdKey(pub PubKey) (key stdcrypto.PublicKey, err error) {
	return crypto.PubKeyToStdKey(pub)
}

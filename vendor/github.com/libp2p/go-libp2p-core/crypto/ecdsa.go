package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"io"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// ECDSAPrivateKey is an implementation of an ECDSA private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSAPrivateKey instead
type ECDSAPrivateKey = crypto.ECDSAPrivateKey

// ECDSAPublicKey is an implementation of an ECDSA public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSAPublicKey instead
type ECDSAPublicKey = crypto.ECDSAPublicKey

// ECDSASig holds the r and s values of an ECDSA signature
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSASig instead
type ECDSASig = crypto.ECDSASig

var (
	// ErrNotECDSAPubKey is returned when the public key passed is not an ecdsa public key
	// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ErrNotECDSAPubKey instead
	ErrNotECDSAPubKey = crypto.ErrNotECDSAPubKey
	// ErrNilSig is returned when the signature is nil
	// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ErrNilSig instead
	ErrNilSig = crypto.ErrNilSig
	// ErrNilPrivateKey is returned when a nil private key is provided
	// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ErrNilPrivateKey instead
	ErrNilPrivateKey = crypto.ErrNilPrivateKey
	// ErrNilPublicKey is returned when a nil public key is provided
	// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ErrNilPublicKey instead
	ErrNilPublicKey = crypto.ErrNilPublicKey
	// ECDSACurve is the default ecdsa curve used
	// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSACurve instead
	ECDSACurve = elliptic.P256()
)

// GenerateECDSAKeyPair generates a new ecdsa private and public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateECDSAKeyPair instead
func GenerateECDSAKeyPair(src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateECDSAKeyPair(src)
}

// GenerateECDSAKeyPairWithCurve generates a new ecdsa private and public key with a speicified curve
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.GenerateECDSAKeyPairWithCurve instead
func GenerateECDSAKeyPairWithCurve(curve elliptic.Curve, src io.Reader) (PrivKey, PubKey, error) {
	return crypto.GenerateECDSAKeyPairWithCurve(curve, src)
}

// ECDSAKeyPairFromKey generates a new ecdsa private and public key from an input private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSAKeyPairFromKey instead
func ECDSAKeyPairFromKey(priv *ecdsa.PrivateKey) (PrivKey, PubKey, error) {
	return crypto.ECDSAKeyPairFromKey(priv)
}

// ECDSAPublicKeyFromPubKey generates a new ecdsa public key from an input public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ECDSAPublicKeyFromPubKey instead
func ECDSAPublicKeyFromPubKey(pub ecdsa.PublicKey) (PubKey, error) {
	return crypto.ECDSAPublicKeyFromPubKey(pub)
}

// MarshalECDSAPrivateKey returns x509 bytes from a private key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MarshalECDSAPrivateKey instead
func MarshalECDSAPrivateKey(ePriv ECDSAPrivateKey) (res []byte, err error) {
	return crypto.MarshalECDSAPrivateKey(ePriv)
}

// MarshalECDSAPublicKey returns x509 bytes from a public key
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MarshalECDSAPublicKey instead
func MarshalECDSAPublicKey(ePub ECDSAPublicKey) (res []byte, err error) {
	return crypto.MarshalECDSAPublicKey(ePub)
}

// UnmarshalECDSAPrivateKey returns a private key from x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MarshalECDSAPrivateKey instead
func UnmarshalECDSAPrivateKey(data []byte) (res PrivKey, err error) {
	return crypto.UnmarshalECDSAPrivateKey(data)
}

// UnmarshalECDSAPublicKey returns the public key from x509 bytes
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.UnmarshalECDSAPublicKey instead
func UnmarshalECDSAPublicKey(data []byte) (key PubKey, err error) {
	return crypto.UnmarshalECDSAPublicKey(data)
}

package crypto

import (
	"github.com/libp2p/go-libp2p/core/crypto"
)

// WeakRsaKeyEnv is an environment variable which, when set, lowers the
// minimum required bits of RSA keys to 512. This should be used exclusively in
// test situations.
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.WeakRsaKeyEnv instead
const WeakRsaKeyEnv = crypto.WeakRsaKeyEnv

// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.MinRsaKeyBits instead
var MinRsaKeyBits = crypto.MinRsaKeyBits

// ErrRsaKeyTooSmall is returned when trying to generate or parse an RSA key
// that's smaller than MinRsaKeyBits bits. In test
// Deprecated: use github.com/libp2p/go-libp2p/core/crypto.ErrRsaKeyTooSmall instead
var ErrRsaKeyTooSmall error

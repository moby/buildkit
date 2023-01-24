package noise

import (
	"errors"
)

// encrypt calls the cipher's encryption. It encrypts the provided plaintext,
// slice-appending the ciphertext on out.
//
// Usually you want to pass a 0-len slice to this method, with enough capacity
// to accommodate the ciphertext in order to spare allocs.
//
// encrypt returns a new slice header, whose len is the length of the resulting
// ciphertext, including the authentication tag.
//
// This method will not allocate if the supplied slice is large enough to
// accommodate the encrypted data + authentication tag. If so, the returned
// slice header should be a view of the original slice.
//
// With the poly1305 MAC function that noise-libp2p uses, the authentication tag
// adds an overhead of 16 bytes.
func (s *secureSession) encrypt(out, plaintext []byte) ([]byte, error) {
	if s.enc == nil {
		return nil, errors.New("cannot encrypt, handshake incomplete")
	}
	return s.enc.Encrypt(out, nil, plaintext)
}

// decrypt calls the cipher's decryption. It decrypts the provided ciphertext,
// slice-appending the plaintext on out.
//
// Usually you want to pass a 0-len slice to this method, with enough capacity
// to accommodate the plaintext in order to spare allocs.
//
// decrypt returns a new slice header, whose len is the length of the resulting
// plaintext, without the authentication tag.
//
// This method will not allocate if the supplied slice is large enough to
// accommodate the plaintext. If so, the returned slice header should be a view
// of the original slice.
func (s *secureSession) decrypt(out, ciphertext []byte) ([]byte, error) {
	if s.dec == nil {
		return nil, errors.New("cannot decrypt, handshake incomplete")
	}
	return s.dec.Decrypt(out, nil, ciphertext)
}

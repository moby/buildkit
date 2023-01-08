package multihash

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
)

// registry is a simple map which maps a multihash indicator number
// to a function : (size:int) -> ((hasher:hash.Hash), (bool:success))
// The function may error (i.e., return (nil, false)) to signify that the hasher can't return that many bytes.
//
// Multihash indicator numbers are reserved and described in
// https://github.com/multiformats/multicodec/blob/master/table.csv .
// The keys used in this map must match those reservations.
//
// Hashers which are available in the golang stdlib will be registered automatically.
// Others can be added using the Register function.
var registry = make(map[uint64]func(int) (h hash.Hash, ok bool))

// Register adds a new hash to the set available from GetHasher and Sum.
//
// Register has a global effect and should only be used at package init time to avoid data races.
//
// The indicator code should be per the numbers reserved and described in
// https://github.com/multiformats/multicodec/blob/master/table.csv .
//
// If Register is called with the same indicator code more than once, the last call wins.
// In practice, this means that if an application has a strong opinion about what implementation to use for a certain hash
// (e.g., perhaps they want to override the sha256 implementation to use a special hand-rolled assembly variant
// rather than the stdlib one which is registered by default),
// then this can be done by making a Register call with that effect at init time in the application's main package.
// This should have the desired effect because the root of the import tree has its init time effect last.
func Register(indicator uint64, hasherFactory func() hash.Hash) {
	if hasherFactory == nil {
		panic("not sensible to attempt to register a nil function")
	}
	maxSize := hasherFactory().Size()
	registry[indicator] = func(size int) (hash.Hash, bool) {
		if size > maxSize {
			return nil, false
		}
		return hasherFactory(), true
	}
	DefaultLengths[indicator] = maxSize
}

// RegisterVariableSize is like Register, but adds a new variable-sized hasher factory that takes a
// size hint.
//
// When passed -1, the hasher should produce digests with the hash-function's default length. When
// passed a non-negative integer, the hasher should try to produce digests of at least the specified
// size.
func RegisterVariableSize(indicator uint64, hasherFactory func(sizeHint int) (hash.Hash, bool)) {
	if hasherFactory == nil {
		panic("not sensible to attempt to register a nil function")
	}

	if hasher, ok := hasherFactory(-1); !ok {
		panic("failed to determine default hash length for hasher")
	} else {
		DefaultLengths[indicator] = hasher.Size()
	}

	registry[indicator] = hasherFactory
}

// GetHasher returns a new hash.Hash according to the indicator code number provided.
//
// The indicator code should be per the numbers reserved and described in
// https://github.com/multiformats/multicodec/blob/master/table.csv .
//
// The actual hashers available are determined by what has been registered.
// The registry automatically contains those hashers which are available in the golang standard libraries
// (which includes md5, sha1, sha256, sha384, sha512, and the "identity" mulithash, among others).
// Other hash implementations can be made available by using the Register function.
// The 'go-mulithash/register/*' packages can also be imported to gain more common hash functions.
//
// If an error is returned, it will match `errors.Is(err, ErrSumNotSupported)`.
func GetHasher(indicator uint64) (hash.Hash, error) {
	return GetVariableHasher(indicator, -1)
}

// GetVariableHasher returns a new hash.Hash according to the indicator code number provided, with
// the specified size hint.
//
// NOTE: The size hint is only a hint. Hashers will attempt to produce at least the number of requested bytes, but may not.
//
// This function can fail if either the hash code is not registered, or the passed size hint is
// statically incompatible with the specified hash function.
func GetVariableHasher(indicator uint64, sizeHint int) (hash.Hash, error) {
	factory, exists := registry[indicator]
	if !exists {
		return nil, fmt.Errorf("unknown multihash code %d (0x%x): %w", indicator, indicator, ErrSumNotSupported)
	}
	hasher, ok := factory(sizeHint)
	if !ok {
		return nil, ErrLenTooLarge
	}
	return hasher, nil
}

// DefaultLengths maps a multihash indicator code to the output size for that hash, in units of bytes.
//
// This map is populated when a hash function is registered by the Register function.
// It's effectively a shortcut for asking Size() on the hash.Hash.
var DefaultLengths = map[uint64]int{}

func init() {
	RegisterVariableSize(IDENTITY, func(_ int) (hash.Hash, bool) { return &identityMultihash{}, true })
	Register(MD5, md5.New)
	Register(SHA1, sha1.New)
	Register(SHA2_224, sha256.New224)
	Register(SHA2_256, sha256.New)
	Register(SHA2_384, sha512.New384)
	Register(SHA2_512, sha512.New)
	Register(SHA2_512_224, sha512.New512_224)
	Register(SHA2_512_256, sha512.New512_256)
	Register(DBL_SHA2_256, func() hash.Hash { return &doubleSha256{sha256.New()} })
}

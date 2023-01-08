/*
	This package has no purpose except to register the blake3 hash function.

	It is meant to be used as a side-effecting import, e.g.

		import (
			_ "github.com/multiformats/go-multihash/register/blake3"
		)
*/
package blake3

import (
	"hash"

	"lukechampine.com/blake3"

	"github.com/multiformats/go-multihash/core"
)

const DefaultSize = 32
const MaxSize = 128

func init() {
	multihash.RegisterVariableSize(multihash.BLAKE3, func(size int) (hash.Hash, bool) {
		if size == -1 {
			size = DefaultSize
		} else if size > MaxSize || size <= 0 {
			return nil, false
		}
		h := blake3.New(size, nil)
		return h, true
	})
}

package zstdchunked

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRollSum(t *testing.T) {
	var buf [100000]uint8
	rnd := rand.New(rand.NewSource(4)) //nolint:gosec // deterministic test data
	for i := range buf {
		buf[i] = uint8(rnd.Intn(256))
	}

	sum := func(offset, length int) uint32 {
		rs := newRollSum()
		for count := offset; count < length; count++ {
			rs.roll(buf[count])
		}
		return rs.digest()
	}

	// the checksum only depends on the last windowSize bytes
	require.Equal(t, sum(0, len(buf)), sum(1, len(buf)))
	require.Equal(t, sum(len(buf)-windowSize*5/2, len(buf)-windowSize), sum(0, len(buf)-windowSize))
	require.Equal(t, sum(0, windowSize+3), sum(3, windowSize+3))
}

func TestRollSumSplit(t *testing.T) {
	rs := newRollSum()
	var splits int
	rnd := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test data
	for range 1 << 20 {
		rs.roll(uint8(rnd.Intn(256)))
		if rs.onSplitWithBits(rollsumBits) {
			splits++
		}
	}
	// on random data, a split is expected every 2^rollsumBits bytes on
	// average
	require.Greater(t, splits, 4)
	require.Less(t, splits, 64)
}

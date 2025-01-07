package disk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDiskStat(t *testing.T) {
	diskStat, err := GetDiskStat("/")
	require.NoError(t, err)
	require.Greater(t, diskStat.Total, int64(0))
	require.GreaterOrEqual(t, diskStat.Free, int64(0))
	require.GreaterOrEqual(t, diskStat.Available, int64(0))
}

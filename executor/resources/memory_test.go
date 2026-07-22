package resources

import (
	"os"
	"path/filepath"
	"testing"

	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMemoryStat(t *testing.T) {
	testDir := t.TempDir()

	memoryStatContents := `anon 24576
file 12791808
kernel_stack 8192
pagetables 4096
sock 2048
shmem 16384
file_mapped 8192
file_dirty 32768
file_writeback 16384
slab 1503104
pgscan 100
pgsteal 99
pgfault 32711
pgmajfault 12`
	err := os.WriteFile(filepath.Join(testDir, memoryStatFile), []byte(memoryStatContents), 0644)
	require.NoError(t, err)

	memoryPressureContents := `some avg10=1.23 avg60=4.56 avg300=7.89 total=3031
full avg10=0.12 avg60=0.34 avg300=0.56 total=9876`
	err = os.WriteFile(filepath.Join(testDir, memoryPressureFile), []byte(memoryPressureContents), 0644)
	require.NoError(t, err)

	memoryEventsContents := `low 4
high 3
max 2
oom 1
oom_kill 5`
	err = os.WriteFile(filepath.Join(testDir, memoryEventsFile), []byte(memoryEventsContents), 0644)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(testDir, memoryPeakFile), []byte("123456"), 0644)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(testDir, memorySwapCurrentFile), []byte("987654"), 0644)
	require.NoError(t, err)

	memoryStat, err := getCgroupMemoryStat(testDir)
	require.NoError(t, err)

	var expectedPressure = &resourcestypes.Pressure{
		Some: &resourcestypes.PressureValues{
			Avg10:  new(1.23),
			Avg60:  new(4.56),
			Avg300: new(7.89),
			Total:  new(uint64(3031)),
		},
		Full: &resourcestypes.PressureValues{
			Avg10:  new(0.12),
			Avg60:  new(0.34),
			Avg300: new(0.56),
			Total:  new(uint64(9876)),
		},
	}

	expectedMemoryStat := &resourcestypes.MemoryStat{
		SwapBytes:     new(uint64(987654)),
		Anon:          new(uint64(24576)),
		File:          new(uint64(12791808)),
		KernelStack:   new(uint64(8192)),
		PageTables:    new(uint64(4096)),
		Sock:          new(uint64(2048)),
		Shmem:         new(uint64(16384)),
		FileMapped:    new(uint64(8192)),
		FileDirty:     new(uint64(32768)),
		FileWriteback: new(uint64(16384)),
		Slab:          new(uint64(1503104)),
		Pgscan:        new(uint64(100)),
		Pgsteal:       new(uint64(99)),
		Pgfault:       new(uint64(32711)),
		Pgmajfault:    new(uint64(12)),
		Peak:          new(uint64(123456)),
		LowEvents:     4,
		HighEvents:    3,
		MaxEvents:     2,
		OomEvents:     1,
		OomKillEvents: 5,
		Pressure:      expectedPressure,
	}
	assert.Equal(t, expectedMemoryStat, memoryStat)
}

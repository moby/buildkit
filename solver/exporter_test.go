package solver

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompareCacheRecord(t *testing.T) {
	now := time.Now()
	a := &CacheRecord{CreatedAt: now, Priority: 1}
	b := &CacheRecord{CreatedAt: now, Priority: 2}
	c := &CacheRecord{CreatedAt: now.Add(1 * time.Second), Priority: 1}
	d := &CacheRecord{CreatedAt: now.Add(-1 * time.Second), Priority: 1}

	records := []*CacheRecord{b, nil, d, a, c, nil}
	slices.SortFunc(records, compareCacheRecord)

	names := map[*CacheRecord]string{
		a:   "a",
		b:   "b",
		c:   "c",
		d:   "d",
		nil: "nil",
	}
	var got []string
	for _, r := range records {
		got = append(got, names[r])
	}
	want := []string{"c", "a", "b", "d", "nil", "nil"}
	require.Truef(t, slices.Equal(got, want), "unexpected order: got %v, want %v", got, want)
}

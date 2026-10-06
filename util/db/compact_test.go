package db

import "testing"

func TestMeetsReclaimThreshold(t *testing.T) {
	opt := CompactOptions{
		MinReclaimBytes:        256 << 20,
		MinReclaimPercent:      30,
		MinReclaimPercentFloor: 10,
	}
	for _, tc := range []struct {
		name        string
		size        int64
		reclaimable int64
		minBytes    int64
		want        bool
	}{
		{name: "large database below floor", size: 100 << 30, reclaimable: 256 << 20},
		{name: "large database at floor", size: 100 << 30, reclaimable: 10 << 30, want: true},
		{name: "small database below trigger", size: 100 << 20, reclaimable: 29 << 20},
		{name: "small database at trigger", size: 100 << 20, reclaimable: 30 << 20, want: true},
		{name: "fraction below floor", size: 101, reclaimable: 10, minBytes: 10},
		{name: "fraction above floor", size: 101, reclaimable: 11, minBytes: 10, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := opt
			if tc.minBytes > 0 {
				check.MinReclaimBytes = tc.minBytes
			}
			if got := check.MeetsReclaimThreshold(tc.size, tc.reclaimable); got != tc.want {
				t.Fatalf("MeetsReclaimThreshold(%d, %d) = %t, want %t", tc.size, tc.reclaimable, got, tc.want)
			}
		})
	}
}

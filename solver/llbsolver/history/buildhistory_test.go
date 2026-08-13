package history

import (
	"testing"

	"github.com/moby/buildkit/cmd/buildkitd/config"
)

func TestQueueEnabled(t *testing.T) {
	tests := []struct {
		name       string
		maxEntries int64
		want       bool
	}{
		{name: "disabled", maxEntries: 0, want: false},
		{name: "enabled", maxEntries: 50, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{MaxEntries: tc.maxEntries}}}
			if got := q.Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

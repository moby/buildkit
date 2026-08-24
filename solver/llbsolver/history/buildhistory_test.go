package history

import (
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestQueueHistoryConfig(t *testing.T) {
	zero := int64(0)
	configured := int64(12)
	tests := []struct {
		name        string
		q           *Queue
		wantEnabled bool
		wantEntries int64
	}{
		{name: "default config", q: &Queue{}, wantEnabled: true, wantEntries: 50},
		{name: "age only", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{}}}, wantEnabled: true},
		{name: "disabled", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{MaxEntries: &zero}}}},
		{name: "configured", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{MaxEntries: &configured}}}, wantEnabled: true, wantEntries: 12},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantEnabled, tc.q.Enabled())
			require.Equal(t, tc.wantEntries, tc.q.maxEntries())
		})
	}
}

func TestStatusSummary(t *testing.T) {
	completed := time.Now()
	first := digest.FromString("first")
	second := digest.FromString("second")
	var summary StatusSummary

	summary.Update(&client.SolveStatus{
		Vertexes: []*client.Vertex{
			{Digest: first, Cached: true},
			{Digest: second, Completed: &completed},
		},
		Warnings: []*client.VertexWarning{{}},
	})
	summary.Update(&client.SolveStatus{
		Vertexes: []*client.Vertex{
			{Digest: first, Cached: true, Completed: &completed},
		},
	})

	require.Equal(t, 1, summary.NumCachedSteps)
	require.Equal(t, 2, summary.NumCompletedSteps)
	require.Equal(t, 2, summary.NumTotalSteps)
	require.Equal(t, 1, summary.NumWarnings)
}

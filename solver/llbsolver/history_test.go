package llbsolver

import (
	"testing"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHistoryFilters(t *testing.T) {
	epoch := time.Now().Add(-24 * time.Hour)
	testRecords := []*controlapi.BuildHistoryEvent{
		{
			Record: &controlapi.BuildHistoryRecord{
				Ref:         "foo123",
				CreatedAt:   timestamppb.New(epoch),
				CompletedAt: timestamppb.New(epoch.Add(time.Minute)),
			},
		},
		{
			Record: &controlapi.BuildHistoryRecord{
				Ref: "bar456",
				FrontendAttrs: map[string]string{
					"context": "https://github.com/user/repo.git#abcdef123",
				},
				CreatedAt: timestamppb.New(epoch.Add(time.Hour)),
			},
		},
		{
			Record: &controlapi.BuildHistoryRecord{
				Ref: "foo789",
				FrontendAttrs: map[string]string{
					"vcs:source": "testrepo",
				},
				CreatedAt:   timestamppb.New(epoch.Add(2 * time.Hour)),
				CompletedAt: timestamppb.New(epoch.Add(2 * time.Hour).Add(10 * time.Minute)),
			},
		},
	}

	tcases := []struct {
		name     string
		filters  []string
		limit    int32
		expected []string
		err      string
	}{
		{
			name:    "no match",
			filters: []string{"ref==foo"},
			limit:   2,
		},
		{
			name:     "ref prefix",
			filters:  []string{"ref~=foo"},
			limit:    2,
			expected: []string{"foo789", "foo123"},
		},
		{
			name:     "repo",
			filters:  []string{"repository!=testrepo"},
			limit:    2,
			expected: []string{"bar456", "foo123"},
		},
		{
			name:     "git context repo",
			filters:  []string{"repository==https://github.com/user/repo.git"},
			limit:    2,
			expected: []string{"bar456"},
		},
		{
			name:     "limit",
			filters:  []string{"ref!=notexist"},
			limit:    2,
			expected: []string{"foo789", "bar456"},
		},
		{
			name:    "invalid filter",
			filters: []string{"ref+foo"},
			err:     "parse error",
		},
		{
			name:     "multi",
			filters:  []string{"repository!=testrepo,ref~=foo"},
			limit:    2,
			expected: []string{"foo123"},
		},
		{
			name:     "latest",
			filters:  []string{"completedAt>23h"},
			expected: []string{"foo789"},
		},
		{
			name:     "oldest",
			filters:  []string{"startedAt<23h"},
			expected: []string{"foo123"},
		},
		{
			name:     "oldestoreq",
			filters:  []string{"startedAt<=23h"},
			expected: []string{"foo123", "bar456"},
		},
		{
			name:     "longest",
			filters:  []string{"duration>5m"},
			expected: []string{"foo789"},
		},
		{
			name:     "multitime",
			filters:  []string{"startedAt>24h,completedAt>22h"},
			expected: []string{"foo789"},
		},
		{
			name:     "multitime or",
			filters:  []string{"startedAt>=23h", "completedAt>22h"},
			expected: []string{"bar456", "foo789"},
		},
		{
			name:     "timeandref",
			filters:  []string{"startedAt>24h,ref~=foo"},
			expected: []string{"foo789"},
		},
		{
			name:     "nofilters",
			limit:    2,
			expected: []string{"foo789", "bar456"},
		},
	}

	for _, tcase := range tcases {
		t.Run(tcase.name, func(t *testing.T) {
			out, err := filterHistoryEvents(testRecords, tcase.filters, tcase.limit)
			if tcase.err != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tcase.err)
				return
			}
			require.NoError(t, err)
			require.Len(t, out, len(tcase.expected))
			var refs []string
			for _, r := range out {
				refs = append(refs, r.Record.Ref)
			}
			require.Equal(t, tcase.expected, refs)
		})
	}
}

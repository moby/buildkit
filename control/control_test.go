package control

import (
	"testing"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDuplicateCacheOptions(t *testing.T) {
	var testCases = []struct {
		name     string
		opts     []*controlapi.CacheOptionsEntry
		expected []*controlapi.CacheOptionsEntry
		rest     []*controlapi.CacheOptionsEntry
	}{
		{
			name: "avoids unique opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: nil,
		},
		{
			name: "finds duplicate opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
		},
		{
			name: "skip inline with attrs",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "inline",
					Attrs: map[string]string{
						"foo": "bar",
					},
				},
			},
			rest: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
			},
			expected: nil,
		},
		{
			name: "skip inline simple",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
				{
					Type: "inline",
				},
			},
			rest: []*controlapi.CacheOptionsEntry{
				{
					Type: "inline",
				},
			},
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rest, result, err := findDuplicateCacheOptions(tc.opts)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.expected, result)
			if tc.rest != nil {
				require.ElementsMatch(t, tc.rest, rest)
			} else if len(result) == 0 {
				require.ElementsMatch(t, tc.opts, rest)
			}
		})
	}
}

func TestParseCacheExportIgnoreError(t *testing.T) {
	tests := map[string]struct {
		expectedIgnoreError bool
		expectedSupported   bool
	}{
		"": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		".": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"fake": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"true": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"True": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"TRUE": {
			expectedIgnoreError: true,
			expectedSupported:   true,
		},
		"truee": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
		"false": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"False": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"FALSE": {
			expectedIgnoreError: false,
			expectedSupported:   true,
		},
		"ffalse": {
			expectedIgnoreError: false,
			expectedSupported:   false,
		},
	}

	for ignoreErrStr, test := range tests {
		t.Run(ignoreErrStr, func(t *testing.T) {
			ignoreErr, supported := parseCacheExportIgnoreError(ignoreErrStr)
			t.Log("checking expectedIgnoreError")
			require.Equal(t, test.expectedIgnoreError, ignoreErr)
			t.Log("checking expectedSupported")
			require.Equal(t, test.expectedSupported, supported)
		})
	}
}

func TestGracefulStopWaitsForActiveBuilds(t *testing.T) {
	c := &Controller{}

	require.NoError(t, c.acquireBuild())

	stopped := make(chan int, 1)
	go func() {
		stopped <- c.GracefulStop()
	}()

	require.Eventually(t, func() bool {
		err := c.acquireBuild()
		if err == nil {
			c.releaseBuild()
			return false
		}
		s, ok := status.FromError(err)
		return ok && s.Code() == codes.Unavailable
	}, time.Second, 10*time.Millisecond)

	select {
	case <-stopped:
		t.Fatal("graceful stop returned before active builds completed")
	case <-time.After(50 * time.Millisecond):
	}

	c.releaseBuild()

	select {
	case active := <-stopped:
		require.Equal(t, 1, active)
	case <-time.After(time.Second):
		t.Fatal("graceful stop did not return after active builds completed")
	}
}

func TestGracefulStopRejectsNewBuilds(t *testing.T) {
	c := &Controller{}

	require.Equal(t, 0, c.GracefulStop())

	err := c.acquireBuild()
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unavailable, s.Code())
}

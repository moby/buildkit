package control

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/solver"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

type releaseCacheManager struct {
	solver.CacheManager
	release func(context.Context) error
}

func (m *releaseCacheManager) ReleaseUnreferenced(ctx context.Context) error {
	return m.release(ctx)
}

func TestReleaseUnreferencedCacheSerializesCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{}, 2)
		unblock := make(chan struct{})
		manager := &releaseCacheManager{release: func(context.Context) error {
			entered <- struct{}{}
			<-unblock
			return nil
		}}
		controller := &Controller{cache: manager, releaseUnreferencedMu: semaphore.NewWeighted(1)}
		done := make(chan error, 2)
		go func() { done <- controller.ReleaseUnreferencedCache(t.Context()) }()
		<-entered
		go func() { done <- controller.ReleaseUnreferencedCache(t.Context()) }()
		synctest.Wait()
		require.Empty(t, entered, "second pass must wait before entering the backend")
		close(unblock)
		require.NoError(t, <-done)
		require.NoError(t, <-done)
		require.Len(t, entered, 1)
	})
}

func TestReleaseUnreferencedCacheCanceledWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		unblock := make(chan struct{})
		expectedErr := errors.New("release failed")
		calls := 0
		controller := &Controller{
			releaseUnreferencedMu: semaphore.NewWeighted(1),
			cache: &releaseCacheManager{release: func(context.Context) error {
				calls++
				if calls == 1 {
					close(started)
					<-unblock
					return expectedErr
				}
				return nil
			}},
		}
		firstDone := make(chan error, 1)
		go func() { firstDone <- controller.ReleaseUnreferencedCache(t.Context()) }()
		<-started
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiterDone := make(chan error, 1)
		go func() { waiterDone <- controller.ReleaseUnreferencedCache(ctx) }()
		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-waiterDone, context.Canceled)
		require.Equal(t, 1, calls, "canceled waiter must not start a pass")
		close(unblock)
		require.ErrorIs(t, <-firstDone, expectedErr)
		require.NoError(t, controller.ReleaseUnreferencedCache(t.Context()))
		require.Equal(t, 2, calls, "failed pass must release the permit")
	})
}

func TestReleaseUnreferencedCacheClosed(t *testing.T) {
	controller := &Controller{
		releaseUnreferencedMu:     semaphore.NewWeighted(1),
		releaseUnreferencedClosed: true,
		cache: &releaseCacheManager{release: func(context.Context) error {
			t.Fatal("closed controller must not access cache storage")
			return nil
		}},
	}
	require.ErrorContains(t, controller.ReleaseUnreferencedCache(t.Context()), "closed")
}

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

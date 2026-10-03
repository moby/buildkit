package solver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type releaseTestStorage struct {
	CacheKeyStorage
	release func(string) error
}

func (s *releaseTestStorage) Release(id string) error {
	return s.release(id)
}

type releaseTestResults struct {
	CacheResultStorage
	exists func(context.Context, string) bool
}

func (s *releaseTestResults) Exists(ctx context.Context, id string) bool {
	return s.exists(ctx, id)
}

func TestReleaseUnreferencedCancellation(t *testing.T) {
	for _, when := range []string{"before walk", "during lookup", "after release"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			storage := NewInMemoryCacheStorage()
			require.NoError(t, storage.AddResult("key", CacheResult{ID: "result"}))
			require.NoError(t, storage.AddResult("key", CacheResult{ID: "other"}))
			released := 0
			cm := &cacheManager{
				backend: &releaseTestStorage{CacheKeyStorage: storage, release: func(string) error {
					released++
					cancel()
					return nil
				}},
				results: &releaseTestResults{exists: func(context.Context, string) bool {
					if when == "during lookup" {
						cancel()
					}
					return false
				}},
			}
			if when == "before walk" {
				cancel()
			}
			require.ErrorIs(t, cm.ReleaseUnreferenced(ctx), context.Canceled)
			expected := 0
			if when == "after release" {
				expected = 1
			}
			require.Equal(t, expected, released)
		})
	}
}

func TestReleaseUnreferencedErrors(t *testing.T) {
	failure := errors.New("storage failure")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{name: "storage error", err: failure, want: failure},
		{name: "already removed", err: ErrNotFound},
		{name: "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := NewInMemoryCacheStorage()
			require.NoError(t, storage.AddResult("a", CacheResult{ID: "missing"}))
			require.NoError(t, storage.AddResult("b", CacheResult{ID: "missing"}))
			require.NoError(t, storage.AddResult("live", CacheResult{ID: "live"}))
			releases := 0
			cm := &cacheManager{
				backend: &releaseTestStorage{CacheKeyStorage: storage, release: func(id string) error {
					require.Equal(t, "missing", id)
					releases++
					return tc.err
				}},
				results: &releaseTestResults{exists: func(_ context.Context, id string) bool {
					return id == "live"
				}},
			}
			err := cm.ReleaseUnreferenced(t.Context())
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, releases, "shared results must be released once per pass")
		})
	}
}

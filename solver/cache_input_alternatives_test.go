package solver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheIDFromAlternativeInputKeys(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "missing-first"
		if reverse {
			name = "matching-first"
		}
		t.Run(name, func(t *testing.T) {
			cache := NewInMemoryCacheManager().(*cacheManager)
			parent, err := cache.Save(NewCacheKey(dgst("parent"), "", 0), testResult("parent"), time.Now())
			require.NoError(t, err)
			content, err := cache.Save(NewCacheKey(dgst("content"), "", 0), testResult("content"), time.Now())
			require.NoError(t, err)
			saved, err := cache.Save(testCacheKey(dgst("copy"), 0, *parent, *content), testResult("copy"), time.Now())
			require.NoError(t, err)

			// A key from another cache may carry both a context key and its content
			// checksum. Only the content key needs to exist in this cache.
			missing := expKey(NewCacheKey(dgst("uncached-context"), "", 0))
			alternatives := depKeys(missing, *content)
			if reverse {
				alternatives = depKeys(*content, missing)
			}
			imported := testCacheKeyWithDeps(dgst("copy"), 0, [][]CacheKeyWithSelector{
				depKeys(*parent), alternatives,
			})
			require.Equal(t, cache.getID(saved.CacheKey), cache.getID(imported))

			// A matching key from another input cannot compensate for an input
			// whose alternatives are all missing.
			unmatched := testCacheKeyWithDeps(dgst("copy"), 0, [][]CacheKeyWithSelector{
				depKeys(*parent), depKeys(missing),
			})
			require.NotEqual(t, cache.getID(saved.CacheKey), cache.getID(unmatched))
		})
	}
}

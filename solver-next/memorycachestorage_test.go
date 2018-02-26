package solver

import "testing"

func TestMemoryCacheStorage(t *testing.T) {
	RunCacheStorageTests(t, func() (CacheKeyStorage, func()) {
		return NewInMemoryCacheStorage(), func() {}
	})
}

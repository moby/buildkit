package solver

import (
	"sync"
)

type kvCacheStorage struct {
	mu sync.RWMutex

	backend CacheKeyStorage
	results CacheResultStorage
}

func newKvCacheStorage(backend CacheKeyStorage, results CacheResultStorage) *kvCacheStorage {
	return &kvCacheStorage{
		backend: backend,
		results: results,
	}
}

package cache

import (
	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/pkg/errors"
)

// Fields to be added:
// Size int64
// AccessTime int64
// Tags
// Descr
// CachePolicy

const sizeUnknown int64 = -1
const keySize = "snapshot.size"
const keyEqualMutable = "cache.equalMutable"
const keyEqualImmutable = "cache.equalImmutable"
const keyCachePolicy = "cache.cachePolicy"

func setSize(si *metadata.StorageItem, s int64) error {
	v, err := metadata.NewValue(s)
	if err != nil {
		return errors.Wrap(err, "failed to create size value")
	}
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keySize, v)
	})
	return nil
}

func getSize(si *metadata.StorageItem) int64 {
	v := si.Get(keySize)
	if v == nil {
		return sizeUnknown
	}
	var size int64
	if err := v.Unmarshal(&size); err != nil {
		return sizeUnknown
	}
	return size
}

func getEqualMutable(si *metadata.StorageItem) string {
	v := si.Get(keyEqualMutable)
	if v == nil {
		return ""
	}
	var str string
	if err := v.Unmarshal(&str); err != nil {
		return ""
	}
	return str
}

func setEqualMutable(si *metadata.StorageItem, s string) error {
	v, err := metadata.NewValue(s)
	if err != nil {
		return errors.Wrapf(err, "failed to create %s meta value", keyEqualMutable)
	}
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keyEqualMutable, v)
	})
	return nil
}

func clearEqualMutable(si *metadata.StorageItem) error {
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keyEqualMutable, nil)
	})
	return nil
}

func setCachePolicy(si *metadata.StorageItem, p cachePolicy) error {
	v, err := metadata.NewValue(p)
	if err != nil {
		return errors.Wrap(err, "failed to create size value")
	}
	return si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, keyCachePolicy, v)
	})
}

func getCachePolicy(si *metadata.StorageItem) cachePolicy {
	v := si.Get(keyCachePolicy)
	if v == nil {
		return cachePolicyDefault
	}
	var p cachePolicy
	if err := v.Unmarshal(&p); err != nil {
		return cachePolicyDefault
	}
	return p
}

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

func setSize(si *metadata.StorageItem, s int64) error {
	v, err := metadata.NewValue(s)
	if err != nil {
		return errors.Wrap(err, "failed to create size value")
	}
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keySize, *v)
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

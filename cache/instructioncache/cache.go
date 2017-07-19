package instructioncache

import (
	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

const cacheKey = "buildkit.instructioncache"

type LocalStore struct {
	MetadataStore *metadata.Store
	Cache         cache.Accessor
}

func (ls *LocalStore) Set(key string, value interface{}) error {
	ref, ok := value.(cache.ImmutableRef)
	if !ok {
		return errors.Errorf("invalid ref")
	}
	v, err := metadata.NewValue(ref.ID())
	if err != nil {
		return err
	}
	v.Index = index(key)
	si, _ := ls.MetadataStore.Get(ref.ID())
	return si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, index(key), v)
	})
}

func (ls *LocalStore) Lookup(ctx context.Context, key string) (interface{}, error) {
	snaps, err := ls.MetadataStore.Search(index(key))
	if err != nil {
		return nil, err
	}

	for _, s := range snaps {
		v := s.Get(index(key))
		if v != nil {
			var id string
			if err = v.Unmarshal(&id); err != nil {
				continue
			}
			r, err := ls.Cache.Get(ctx, id)
			if err != nil {
				logrus.Warnf("failed to get cached snapshot %s: %v", id, err)
				continue
			}
			return r, nil
		}
	}
	return nil, nil
}

func index(k string) string {
	return cacheKey + "::" + k
}

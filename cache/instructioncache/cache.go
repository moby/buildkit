package instructioncache

import (
	"bytes"

	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

const cacheKey = "buildkit.instructioncache"
const contentCacheKey = "buildkit.instructioncache.content"

const mappingBucket = "_contentMapping"

type LocalStore struct {
	MetadataStore *metadata.Store
	Cache         cache.Accessor
}

func (ls *LocalStore) Set(key digest.Digest, value interface{}) error {
	ref, ok := value.(cache.ImmutableRef)
	if !ok {
		return errors.Errorf("invalid ref")
	}
	v, err := metadata.NewValue(ref.ID())
	if err != nil {
		return err
	}
	v.Index = index(key.String())
	si, _ := ls.MetadataStore.Get(ref.ID())
	return si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, v.Index, v)
	})
}
func (ls *LocalStore) Probe(ctx context.Context, key digest.Digest) (bool, error) {
	return ls.MetadataStore.Probe(index(key.String()))
}

func (ls *LocalStore) Lookup(ctx context.Context, key digest.Digest, msg string) (interface{}, error) {
	snaps, err := ls.MetadataStore.Search(index(key.String()))
	if err != nil {
		return nil, err
	}

	for _, s := range snaps {
		v := s.Get(index(key.String()))
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

func (ls *LocalStore) SetContentMapping(contentKey, regularKey digest.Digest) error {
	db := ls.MetadataStore.DB()
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(mappingBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(contentKey.String()+"::"+regularKey.String()), []byte{})
	})
}

func (ls *LocalStore) GetContentMapping(key digest.Digest) ([]digest.Digest, error) {
	var dgsts []digest.Digest
	db := ls.MetadataStore.DB()
	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(mappingBucket))
		if b == nil {
			return nil
		}

		c := b.Cursor()
		index := []byte(key.String() + "::")
		k, _ := c.Seek(index)
		for {
			if k != nil && bytes.HasPrefix(k, index) {
				dgsts = append(dgsts, digest.Digest(string(bytes.TrimPrefix(k, index))))
				k, _ = c.Next()
			} else {
				break
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return dgsts, nil
}

func index(k string) string {
	return cacheKey + "::" + k
}

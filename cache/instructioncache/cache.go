package instructioncache

import (
	"strings"

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

func (ls *LocalStore) Lookup(ctx context.Context, key digest.Digest) (interface{}, error) {
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

func (ls *LocalStore) SetContentMapping(key digest.Digest, value interface{}) error {
	ref, ok := value.(cache.ImmutableRef)
	if !ok {
		return errors.Errorf("invalid ref")
	}
	v, err := metadata.NewValue(ref.ID())
	if err != nil {
		return err
	}
	v.Index = contentIndex(key.String())
	si, _ := ls.MetadataStore.Get(ref.ID())
	return si.Update(func(b *bolt.Bucket) error {
		return si.SetValue(b, v.Index, v)
	})
}

func (ls *LocalStore) GetContentMapping(key digest.Digest) ([]digest.Digest, error) {
	snaps, err := ls.MetadataStore.Search(contentIndex(key.String()))
	if err != nil {
		return nil, err
	}
	var out []digest.Digest
	for _, s := range snaps {
		for _, k := range s.Keys() {
			if strings.HasPrefix(k, index("")) {
				out = append(out, digest.Digest(strings.TrimPrefix(k, index("")))) // TODO: type
			}
		}
	}
	return out, nil
}

func index(k string) string {
	return cacheKey + "::" + k
}

func contentIndex(k string) string {
	return contentCacheKey + "::" + k
}

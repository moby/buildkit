package instructioncache

import (
	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

const cacheKey = "buildkit.instructioncache"

type cacheGroup struct {
	Snapshots []string `json:"snapshots"`
}

type LocalStore struct {
	MetadataStore *metadata.Store
	Cache         cache.Accessor
}

func (ls *LocalStore) Set(key string, refsAny []interface{}) error {
	refs, err := toReferenceArray(refsAny)
	if err != nil {
		return err
	}
	cg := cacheGroup{}
	for _, r := range refs {
		cg.Snapshots = append(cg.Snapshots, r.ID())
	}
	v, err := metadata.NewValue(cg)
	if err != nil {
		return err
	}
	v.Index = index(key)
	for _, r := range refs {
		si, _ := ls.MetadataStore.Get(r.ID())
		if err := si.Update(func(b *bolt.Bucket) error { // TODO: should share transaction
			return si.SetValue(b, index(key), *v)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (ls *LocalStore) Lookup(ctx context.Context, key string) ([]interface{}, error) {
	snaps, err := ls.MetadataStore.Search(index(key))
	if err != nil {
		return nil, err
	}
	refs := make([]cache.ImmutableRef, 0)
	var retErr error
loop0:
	for _, s := range snaps {
		retErr = nil
		for _, r := range refs {
			r.Release(context.TODO())
		}
		refs = nil

		v := s.Get(index(key))
		if v != nil {
			var cg cacheGroup
			if err = v.Unmarshal(&cg); err != nil {
				retErr = err
				continue
			}
			for _, id := range cg.Snapshots {
				r, err := ls.Cache.Get(ctx, id)
				if err != nil {
					retErr = err
					continue loop0
				}
				refs = append(refs, r)
			}
			retErr = nil
			break
		}
	}
	if retErr != nil {
		for _, r := range refs {
			r.Release(context.TODO())
		}
		refs = nil
	}
	return toAny(refs), retErr
}

func index(k string) string {
	return cacheKey + "::" + k
}

func toReferenceArray(in []interface{}) ([]cache.ImmutableRef, error) {
	out := make([]cache.ImmutableRef, 0, len(in))
	for _, i := range in {
		r, ok := i.(cache.ImmutableRef)
		if !ok {
			return nil, errors.Errorf("invalid reference")
		}
		out = append(out, r)
	}
	return out, nil
}

func toAny(in []cache.ImmutableRef) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, i := range in {
		out = append(out, i)
	}
	return out
}

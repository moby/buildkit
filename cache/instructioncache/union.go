package instructioncache

import (
	"golang.org/x/net/context"

	digest "github.com/opencontainers/go-digest"
)

// Union creates a union of two caches.
// Set operations affects only on the base one.
func Union(base, another InstructionCache) InstructionCache {
	return &union{base: base, another: another}
}

type union struct {
	base    InstructionCache
	another InstructionCache
}

func (u *union) Probe(ctx context.Context, key digest.Digest) (bool, error) {
	v, err := u.base.Probe(ctx, key)
	if err != nil {
		return false, err
	}
	if v {
		return v, nil
	}
	return u.another.Probe(ctx, key)
}

func (u *union) Lookup(ctx context.Context, key digest.Digest, msg string) (interface{}, error) {
	v, err := u.base.Probe(ctx, key)
	if err != nil {
		return false, err
	}
	if v {
		return u.base.Lookup(ctx, key, msg)
	}
	return u.another.Lookup(ctx, key, msg)
}
func (u *union) Set(key digest.Digest, ref interface{}) error {
	return u.base.Set(key, ref)
}
func (u *union) SetContentMapping(contentKey, key digest.Digest) error {
	return u.base.SetContentMapping(contentKey, key)
}
func (u *union) GetContentMapping(dgst digest.Digest) ([]digest.Digest, error) {
	localKeys, err := u.base.GetContentMapping(dgst)
	if err != nil {
		return nil, err
	}
	remoteKeys, err := u.another.GetContentMapping(dgst)
	if err != nil {
		return nil, err
	}
	return append(localKeys, remoteKeys...), nil
}

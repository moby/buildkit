package util

import (
	"fmt"

	"github.com/mitchellh/hashstructure/v2"

	controlapi "github.com/moby/buildkit/api/services/control"
)

// DedupCacheOptions removes duplicates from cacheOpts
func DedupCacheOptions(cacheOpts []*controlapi.CacheOptionsEntry) (result []*controlapi.CacheOptionsEntry, err error) {
	seen := map[string]*controlapi.CacheOptionsEntry{}
	for _, opt := range cacheOpts {
		key, err := cacheOptKey(*opt)
		if err != nil {
			return nil, err
		}
		seen[key] = opt
	}
	result = make([]*controlapi.CacheOptionsEntry, 0, len(seen))
	for _, opt := range seen {
		result = append(result, opt)
	}
	return result, nil
}

func cacheOptKey(opt controlapi.CacheOptionsEntry) (string, error) {
	if opt.Type == "registry" && opt.Attrs["ref"] != "" {
		return opt.Attrs["ref"], nil
	}
	var rawOpt = struct {
		Type  string
		Attrs map[string]string
	}{
		Type:  opt.Type,
		Attrs: opt.Attrs,
	}
	hash, err := hashstructure.Hash(rawOpt, hashstructure.FormatV2, nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(opt.Type, ":", hash), nil
}

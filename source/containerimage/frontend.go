package containerimage

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

const frontendImagesBucket = "frontend.images"

func (is *Source) PruneFrontendResolutions(ctx context.Context) error {
	is.frontendMu.Lock()
	defer is.frontendMu.Unlock()

	resolutions := make(map[string]digest.Digest)
	if err := is.MetadataStore.DB().View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(frontendImagesBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			resolutions[string(k)] = digest.Digest(v)
			return nil
		})
	}); err != nil {
		return errors.Wrap(err, "failed to read frontend image resolutions")
	}
	var stale []string
	for ref, dgst := range resolutions {
		if _, err := is.ContentStore.Info(ctx, dgst); err != nil {
			if !errors.Is(err, cerrdefs.ErrNotFound) {
				return err
			}
			stale = append(stale, ref)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return errors.Wrap(is.MetadataStore.DB().Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(frontendImagesBucket))
		for _, ref := range stale {
			if err := b.Delete([]byte(ref)); err != nil {
				return err
			}
		}
		return nil
	}), "failed to prune frontend image resolutions")
}

func (is *Source) frontendDigest(ref string) (digest.Digest, error) {
	var dgst digest.Digest
	err := is.MetadataStore.DB().View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(frontendImagesBucket))
		if b == nil {
			return nil
		}
		if value := b.Get([]byte(ref)); value != nil {
			var err error
			dgst, err = digest.Parse(string(value))
			return err
		}
		return nil
	})
	return dgst, errors.Wrap(err, "failed to read frontend image resolution")
}

func (is *Source) setFrontendDigest(ref string, dgst digest.Digest) error {
	is.frontendMu.Lock()
	defer is.frontendMu.Unlock()

	return errors.Wrap(is.MetadataStore.DB().Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(frontendImagesBucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(ref), []byte(dgst))
	}), "failed to save frontend image resolution")
}

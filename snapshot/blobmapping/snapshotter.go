package blobmapping

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const dbFile = "blobmap.db"

var (
	bucketBySnapshot = []byte("by_snapshot")
	bucketByBlob     = []byte("by_blob")
)

type Opt struct {
	Content     content.Store
	Snapshotter snapshot.Snapshotter
	Root        string
}

type Info struct {
	snapshot.Info
	Blob string
}

// this snapshotter keeps an internal mapping between a snapshot and a blob

type Snapshotter struct {
	snapshot.Snapshotter
	db  *bolt.DB
	opt Opt
}

func NewSnapshotter(opt Opt) (*Snapshotter, error) {
	if err := os.MkdirAll(opt.Root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", opt.Root)
	}

	p := filepath.Join(opt.Root, dbFile)
	db, err := bolt.Open(p, 0600, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open database file %s", p)
	}

	s := &Snapshotter{
		Snapshotter: opt.Snapshotter,
		db:          db,
		opt:         opt,
	}

	return s, nil
}

func (s *Snapshotter) init() error {
	// this should do a walk from the DB and remove any records that are not
	// in snapshotter any more
	return nil
}

// Remove also removes a refrence to a blob. If it is a last reference then it deletes it the blob as well
// Remove is not safe to be called concurrently
func (s *Snapshotter) Remove(ctx context.Context, key string) error {
	blob, err := s.GetBlob(ctx, key)
	if err != nil {
		return err
	}

	if err := s.Snapshotter.Remove(ctx, key); err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBySnapshot)
		if b == nil {
			return nil
		}
		b.Delete([]byte(key))

		if blob != "" {
			b = tx.Bucket(bucketByBlob)
			b.Delete(blobKey(blob, key))
			if len(keyRange(tx, blobKey(blob, ""))) == 0 { // last snapshot
				s.opt.Content.Delete(ctx, digest.Digest(blob)) // log error
			}
		}
		return nil
	})
}

// TODO: make Blob/SetBlob part of generic metadata wrapper that can detect
// blob key for deletion logic

func (s *Snapshotter) GetBlob(ctx context.Context, key string) (digest.Digest, error) {
	var blob digest.Digest
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBySnapshot)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v != nil {
			blob = digest.Digest(v)
		}
		return nil
	})
	return blob, err
}

// Validates that there is no blob associated with the snapshot.
// Checks that there is a blob in the content store.
// If same blob has already been set then this is a noop.
func (s *Snapshotter) SetBlob(ctx context.Context, key string, blob digest.Digest) error {
	_, err := s.opt.Content.Info(ctx, blob)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketBySnapshot)
		if err != nil {
			return err
		}
		v := b.Get([]byte(key))
		if v != nil {
			if string(v) != string(blob) {
				return errors.Errorf("different blob already set for %s", key)
			} else {
				return nil
			}
		}

		if err := b.Put([]byte(key), []byte(blob)); err != nil {
			return err
		}
		b, err = tx.CreateBucketIfNotExists(bucketByBlob)
		if err != nil {
			return err
		}
		return b.Put(blobKey(blob, key), []byte{})
	})
}

func blobKey(blob digest.Digest, snapshot string) []byte {
	return []byte(string(blob) + "-" + snapshot)
}

// results are only valid for the lifetime of the transaction
func keyRange(tx *bolt.Tx, key []byte) (out [][]byte) {
	c := tx.Cursor()
	lastKey := append([]byte{}, key...)
	lastKey = append(lastKey, ^byte(0))
	k, _ := c.Seek([]byte(key))
	for {
		if k != nil && bytes.Compare(k, lastKey) <= 0 {
			out = append(out, k)
			continue
		}
		break
	}
	return
}

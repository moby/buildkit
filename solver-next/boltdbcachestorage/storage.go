package boltdbcachestorage

import (
	"bytes"
	"encoding/json"

	"github.com/boltdb/bolt"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/pkg/errors"
)

const (
	mainBucket   = "_main"
	resultBucket = "_result"
	linksBucket  = "_links"
)

type Store struct {
	db *bolt.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open database file %s", dbPath)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range []string{mainBucket, resultBucket, linksBucket} {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Get(id string) (solver.CacheKeyInfo, error) {
	var cki solver.CacheKeyInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(mainBucket))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		v := b.Get([]byte(id))
		if v == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		return json.Unmarshal(v, &cki)
	})
	if err != nil {
		return solver.CacheKeyInfo{}, err
	}
	return cki, nil
}

func (s *Store) Set(info solver.CacheKeyInfo) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(mainBucket))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		dt, err := json.Marshal(info)
		if err != nil {
			return err
		}
		return b.Put([]byte(info.ID), dt)
	})
}

func (s *Store) WalkResults(id string, fn func(solver.CacheResult) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resultBucket))
		if b == nil {
			return nil
		}
		b = b.Bucket([]byte(id))
		if b == nil {
			return nil
		}
		if err := b.ForEach(func(k, v []byte) error {
			var res solver.CacheResult
			if err := json.Unmarshal(v, &res); err != nil {
				return err
			}
			return fn(res)
		}); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) Load(id string, resultID string) (solver.CacheResult, error) {
	var res solver.CacheResult
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resultBucket))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		b = b.Bucket([]byte(id))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}

		v := b.Get([]byte(resultID))
		if v == nil {
			return errors.WithStack(solver.ErrNotFound)
		}

		return json.Unmarshal(v, &res)
	}); err != nil {
		return solver.CacheResult{}, err
	}
	return res, nil
}

func (s *Store) AddResult(id string, res solver.CacheResult) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(resultBucket))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		b, err := b.CreateBucketIfNotExists([]byte(id))
		if err != nil {
			return err
		}
		dt, err := json.Marshal(res)
		if err != nil {
			return err
		}
		return b.Put([]byte(res.ID), dt)
	})
}

func (s *Store) Release(id, resultID string) error {
	return errors.Errorf("not-implemented")
}

func (s *Store) AddLink(id string, link solver.CacheInfoLink, target string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(linksBucket))
		if b == nil {
			return errors.WithStack(solver.ErrNotFound)
		}
		b, err := b.CreateBucketIfNotExists([]byte(id))
		if err != nil {
			return err
		}

		dt, err := json.Marshal(link)
		if err != nil {
			return err
		}

		return b.Put(bytes.Join([][]byte{dt, []byte(target)}, []byte("@")), []byte{})
	})
}

func (s *Store) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(linksBucket))
		if b == nil {
			return nil
		}
		b = b.Bucket([]byte(id))
		if b == nil {
			return nil
		}

		dt, err := json.Marshal(link)
		if err != nil {
			return err
		}

		index := bytes.Join([][]byte{dt, {}}, []byte("@"))
		c := b.Cursor()
		k, _ := c.Seek([]byte(index))
		for {
			if k != nil && bytes.HasPrefix(k, index) {
				target := bytes.TrimPrefix(k, index)

				if err := fn(string(target)); err != nil {
					return err
				}

				k, _ = c.Next()
			} else {
				break
			}
		}

		return nil
	}); err != nil {
		return err
	}
	return nil
}

package sqlcachestorage

import (
	"context"
	"database/sql"
	"time"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sqliteOpen(dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.AutoMigrate(); err != nil {
		bklog.G(context.TODO()).WithError(err).Error("unable to initialize sql store")
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) AutoMigrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, query := range createSQL {
		if _, err := tx.Exec(query); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Exists(id string) bool {
	res, err := s.db.Query(existsSQL, id)
	if err != nil {
		return false
	}
	defer res.Close()

	return res.Next()
}

func (s *Store) Walk(fn func(id string) error) error {
	ids, err := func() (ids []string, err error) {
		res, err := s.db.Query(walkSQL)
		if err != nil {
			return nil, err
		}
		defer res.Close()

		for res.Next() {
			var id string
			if err := res.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, nil
	}()
	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) WalkResults(id string, fn func(solver.CacheResult) error) (_ error) {
	results, err := func() (results []solver.CacheResult, err error) {
		res, err := s.db.Query(walkResultsSQL, id)
		if err != nil {
			return nil, err
		}
		defer res.Close()

		for res.Next() {
			var (
				id        string
				createdAt time.Time
			)
			if err := res.Scan(&id, &createdAt); err != nil {
				return nil, err
			}
			results = append(results, solver.CacheResult{
				CreatedAt: createdAt,
				ID:        id,
			})
		}
		return results, nil
	}()
	if err != nil {
		return err
	}

	for _, res := range results {
		if err := fn(res); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Load(id string, resultID string) (solver.CacheResult, error) {
	res := s.db.QueryRow(loadSQL, id, resultID)

	var cacheRes solver.CacheResult
	if err := res.Scan(&cacheRes.ID, &cacheRes.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.WithStack(solver.ErrNotFound)
		}
		return solver.CacheResult{}, err
	}
	return cacheRes, nil
}

func (s *Store) AddResult(id string, res solver.CacheResult) error {
	_, err := s.db.Exec(addResultSQL, id, res.CreatedAt, res.ID)
	return err
}

func (s *Store) Release(resultID string) error {
	_, err := s.db.Exec(deleteResultByRefIDSQL, resultID)
	return err
}

func (s *Store) WalkIDsByResult(resultID string, fn func(string) error) error {
	ids, err := func() (ids []string, err error) {
		res, err := s.db.Query(walkIDsByResultSQL, resultID)
		if err != nil {
			return nil, err
		}
		defer res.Close()

		for res.Next() {
			var id string
			if err := res.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, nil
	}()
	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddLink(id string, link solver.CacheInfoLink, target string) error {
	_, err := s.db.Exec(addLinkSQL, id, link.Input, link.Output, string(link.Digest), string(link.Selector), target)
	return err
}

func (s *Store) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	ids, err := func() (ids []string, err error) {
		res, err := s.db.Query(walkLinksSQL, id, link.Input, link.Output, string(link.Digest), string(link.Selector))
		if err != nil {
			return nil, err
		}
		defer res.Close()

		for res.Next() {
			var id string
			if err := res.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, nil
	}()
	if err != nil {
		return err
	}

	for _, id := range ids {
		if err := fn(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) HasLink(id string, link solver.CacheInfoLink, target string) bool {
	res, err := s.db.Query(hasLinkSQL, id, link.Input, link.Output, link.Digest, link.Selector, target)
	if err != nil {
		return false
	}
	defer res.Close()

	return res.Next()
}

func (s *Store) WalkBacklinks(id string, fn func(id string, link solver.CacheInfoLink) error) (_ error) {
	ids, links, err := func() (ids []string, links []solver.CacheInfoLink, err error) {
		res, err := s.db.Query(walkBacklinksSQL, id)
		if err != nil {
			return nil, nil, err
		}
		defer res.Close()

		for res.Next() {
			var (
				id   string
				link solver.CacheInfoLink
			)
			if err := res.Scan(&id, &link.Input, &link.Output, &link.Digest, &link.Selector); err != nil {
				return nil, nil, err
			}
			ids = append(ids, id)
			links = append(links, link)
		}
		return ids, links, nil
	}()
	if err != nil {
		return err
	}

	for i, id := range ids {
		if err := fn(id, links[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

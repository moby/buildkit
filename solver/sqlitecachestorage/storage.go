package sqlitecachestorage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/cachedigest"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/iterutil"
	digest "github.com/opencontainers/go-digest"
)

type Store struct {
	db      *sql.DB
	results solver.CacheResultStorage
}

func NewStore(path string, results solver.CacheResultStorage) (*Store, error) {
	db, err := sqliteOpen(path)
	if err != nil {
		return nil, err
	}

	s := &Store{
		db:      db,
		results: results,
	}
	if err := s.autoMigrate(); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) autoMigrate() error {
	for _, stmt := range []string{
		createTableRecordsSQL,
		createTableLinksSQL,
		createIndexLinksSourceRecordSQL,
		createIndexRecordsIDSQL,
		createIndexRecordsIDAndRecordSQL,
		createIndexSearchLinksSQL,
	} {
		if _, err := s.db.ExecContext(context.TODO(), stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Query(deps []solver.CacheKeyWithSelector, input solver.Index, dgst digest.Digest, output solver.Index) ([]*solver.CacheKey, error) {
	if len(deps) == 0 {
		var exists int
		id := rootKey(dgst, output)
		if err := s.db.QueryRowContext(context.TODO(), linkExistsSQL, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		return []*solver.CacheKey{{ID: id.String()}}, nil
	}

	data := struct {
		Deps   []solver.CacheKeyWithSelector
		Digest string
		Output int
		Input  int
	}{
		Deps:   deps,
		Digest: string(dgst),
		Output: int(output),
		Input:  int(input),
	}

	rows, err := queryLinksSQL().QueryContext(context.TODO(), s.db, data)
	if err != nil {
		return nil, err
	}

	var keys []*solver.CacheKey
	for rows.Next() {
		key := &solver.CacheKey{}
		if err := rows.Scan(&key.ID); err != nil {
			rows.Close()
			return nil, err
		}
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}
	return keys, rows.Err()
}

func (s *Store) Records(ctx context.Context, ck *solver.CacheKey) ([]*solver.CacheRecord, error) {
	rows, err := s.db.QueryContext(ctx, selectRecordsSQL, ck.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]*solver.CacheRecord, 0)
	for rows.Next() {
		var record solver.CacheRecord
		if err := rows.Scan(&record.ID, &record.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) Load(ctx context.Context, key *solver.CacheKey, id string) (solver.Result, error) {
	res := solver.CacheResult{ID: id}
	if err := s.db.QueryRowContext(ctx, selectRecordSQL, key.ID, res.ID).Scan(&res.CreatedAt); err != nil {
		return nil, err
	}
	return s.results.Load(ctx, res)
}

func (s *Store) LoadWithParents(ctx context.Context, key *solver.CacheKey, id string) ([]solver.LoadedResult, error) {
	return nil, solver.ErrNotImplemented
}

func (s *Store) LoadRemotes(ctx context.Context, key *solver.CacheKey, id string, compression *compression.Config, sg session.Group) ([]*solver.Remote, error) {
	res := solver.CacheResult{ID: id}
	if err := s.db.QueryRowContext(ctx, selectRecordSQL, key.ID, res.ID).Scan(&res.CreatedAt); err != nil {
		return nil, err
	}
	return s.results.LoadRemotes(ctx, res, compression, sg)
}

func (s *Store) Save(k *solver.CacheKey, r solver.Result, createdAt time.Time) (_ *solver.CacheRecord, retErr error) {
	res, err := s.results.Save(r, createdAt)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(context.TODO(), nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(context.TODO(), insertRecordSQL, k.ID, res.CreatedAt, res.ID); err != nil {
		return nil, err
	}

	type link struct {
		Source *solver.CacheKey
		Link   solver.CacheInfoLink
		Target string
	}

	getLinks := func(k *solver.CacheKey) (links []link) {
		for i, deps := range k.Deps() {
			for _, ck := range deps {
				l := solver.CacheInfoLink{
					Input:    solver.Index(i),
					Output:   k.Output(),
					Digest:   k.Digest(),
					Selector: ck.Selector,
				}
				links = append(links, link{
					Source: ck.CacheKey.CacheKey,
					Link:   l,
					Target: k.ID,
				})
			}
		}
		return links
	}

	stmt, err := tx.PrepareContext(context.TODO(), insertLinkSQL)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for pending := getLinks(k); len(pending) > 0; {
		l := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		if _, err := stmt.ExecContext(context.TODO(), l.Source.ID, l.Link.Digest, int(l.Link.Output), int(l.Link.Input), l.Target, l.Link.Selector); err != nil {
			return nil, err
		}
		pending = append(pending, getLinks(l.Source)...)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &solver.CacheRecord{
		ID:        res.ID,
		CreatedAt: res.CreatedAt,
	}, nil
}

func (s *Store) Parents(ctx context.Context, id string) iterutil.FallibleSeq2[string, solver.CacheInfoLink] {
	return iterutil.FallibleSeq2Func(func(yield func(string, solver.CacheInfoLink) bool) error {
		type elem struct {
			Key   string
			Value solver.CacheInfoLink
		}

		elems, err := func() (elems []elem, _ error) {
			stmt, err := s.db.QueryContext(ctx, queryBacklinksSQL, id)
			if err != nil {
				return nil, err
			}
			defer stmt.Close()

			for stmt.Next() {
				var e elem
				if err := stmt.Scan(&e.Key, &e.Value.Input, &e.Value.Output, &e.Value.Digest, &e.Value.Selector); err != nil {
					return nil, err
				}
				elems = append(elems, e)
			}

			if err := stmt.Close(); err != nil {
				return nil, err
			}
			return elems, stmt.Err()
		}()
		if err != nil {
			return err
		}

		for _, e := range elems {
			if !yield(e.Key, e.Value) {
				break
			}
		}
		return nil
	})
}

func (s *Store) AlternativeRoots(ctx context.Context, key *solver.CacheKey, rec *solver.CacheRecord) iterutil.FallibleSeq[string] {
	return iterutil.FallibleSeqFunc(func(yield func(string) bool) error {
		rows, err := s.db.QueryContext(ctx, queryAlternativeRootsSQL, key.ID, rec.ID)
		if err != nil {
			return err
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}

		if err := rows.Close(); err != nil {
			return err
		}

		if err := rows.Err(); err != nil {
			return err
		}

		for _, id := range ids {
			if !yield(id) {
				break
			}
		}
		return nil
	})
}

func (s *Store) ReleaseUnreferenced(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, queryDistinctRecordsSQL)
	if err != nil {
		return err
	}

	var missing []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}

		if !s.results.Exists(ctx, id) {
			missing = append(missing, id)
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if len(missing) == 0 {
		return nil
	}

	// Delete cache records that do not reference a valid record.
	res, err := deleteRecordsSQL().ExecContext(ctx, tx, missing)
	if err != nil {
		return err
	}

	if n, err := res.RowsAffected(); err != nil || n == 0 {
		// Nothing was deleted.
		return err
	}

	stmt, err := tx.PrepareContext(ctx, deleteUnreferencedLinksSQL)
	if err != nil {
		return err
	}

	for {
		res, err := stmt.ExecContext(ctx)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			break
		}
	}

	// Commit the transaction.
	return tx.Commit()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func rootKey(dgst digest.Digest, output solver.Index) digest.Digest {
	out, _ := cachedigest.FromBytes(fmt.Appendf(nil, "%s@%d", dgst, output), cachedigest.TypeString)
	if strings.HasPrefix(dgst.String(), "random:") {
		return digest.Digest("random:" + dgst.Encoded())
	}
	return out
}

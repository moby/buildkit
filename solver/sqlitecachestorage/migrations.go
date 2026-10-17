package sqlitecachestorage

import (
	"context"
	"database/sql"

	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (
	version = "v1.0"

	createTableSchemaMigrationsSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
	version text NOT NULL
)
`

	insertSchemaVersionSQL = `
INSERT INTO schema_version (version) VALUES (?)
`

	updateSchemaVersionSQL = `
UPDATE schema_version SET version = ?
`
)

type migration struct {
	From, To string
}

// migrations holds a method of migrating between databases.
// This mapping should only hold the migrations necessary to get
// any currently existing version to the current version that is
// marked above.
var migrations = map[migration][]string{
	{To: "v1.0"}: {
		createTableLinksSQL,
		createTableRecordsSQL,
		createIndexLinksSourceRecordSQL,
		createIndexRecordsIDSQL,
		createIndexRecordsIDAndRecordSQL,
		createIndexSearchLinksSQL,
	},
}

func Migrate(ctx context.Context, db *sql.DB) error {
	return migrate(ctx, db, version)
}

func migrate(ctx context.Context, db *sql.DB, version string) error {
	if _, err := db.ExecContext(ctx, createTableSchemaMigrationsSQL); err != nil {
		return err
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return err
	}

	bklog.G(ctx).WithFields(logrus.Fields{
		"current": current,
		"desired": version,
	}).Info("checking database schema")

	for current != version {
		path := migration{current, version}
		stmts, ok := migrations[path]
		if !ok {
			bklog.G(ctx).Errorf("migration failed; no path from %q to %q", current, version)
			return errors.Errorf("migration failed")
		}

		bklog.G(ctx).WithField("version", version).Info("migrating database")
		if err := doMigration(ctx, db, path, stmts); err != nil {
			return err
		}
		current = version
	}

	bklog.G(ctx).WithField("version", version).Info("database schema is up to date")
	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (string, error) {
	row := db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`)

	var version string
	if err := row.Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No current version.
			return "", nil
		}
		return "", err
	}
	return version, nil
}

func doMigration(ctx context.Context, db *sql.DB, path migration, stmts []string) (retErr error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := tx.Rollback(); err != nil {
				bklog.G(ctx).WithError(err).Error("failed to rollback transaction")
			}
		}
	}()

	for _, query := range stmts {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}

	var updateSchemaSQL string
	if path.From != "" {
		updateSchemaSQL = updateSchemaVersionSQL
	} else {
		updateSchemaSQL = insertSchemaVersionSQL
	}

	if _, err := tx.ExecContext(ctx, updateSchemaSQL, path.To); err != nil {
		return err
	}
	return tx.Commit()
}

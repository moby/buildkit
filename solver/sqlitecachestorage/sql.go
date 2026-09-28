package sqlitecachestorage

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"text/template"
)

const (
	createTableLinksSQL = `
CREATE TABLE IF NOT EXISTS cache_links (
	source_record text NOT NULL,
	digest text NOT NULL,
	output_index NOT NULL,
	input_index integer NOT NULL,
	selector text NOT NULL DEFAULT "",
	target_record text NOT NULL,
	UNIQUE (source_record, digest, output_index, input_index, selector, target_record)
);
`

	createTableRecordsSQL = `
CREATE TABLE IF NOT EXISTS cache_records (
	id text NOT NULL,
	record_id NOT NULL,
	created_at datetime,
	UNIQUE (id, record_id)
);
`

	createIndexLinksSourceRecordSQL = `
CREATE INDEX IF NOT EXISTS cache_links_source_record_index
ON cache_links (source_record);
`

	createIndexRecordsIDSQL = `
CREATE INDEX IF NOT EXISTS cache_records_id_index
ON cache_records (id);
`

	createIndexRecordsIDAndRecordSQL = `
CREATE INDEX IF NOT EXISTS cache_records_id_index
ON cache_records (id, record_id);
`

	insertLinkSQL = `
INSERT INTO cache_links (source_record, digest, output_index, input_index, target_record, selector)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;
`

	insertRecordSQL = `
INSERT INTO cache_records (id, created_at, record_id)
VALUES (?, ?, ?)
ON CONFLICT DO NOTHING;
`

	linkExistsSQL = `
SELECT 1 FROM cache_links
WHERE source_record = ?
LIMIT 1;
`

	queryLinksSQL = `
SELECT target_record
FROM cache_links
WHERE source_record = ?
  AND digest = ?
	AND output_index = ?
  AND input_index = ?
	AND selector = ?
ORDER BY target_record ASC;
`

	selectRecordsSQL = `
SELECT record_id, created_at
FROM cache_records
WHERE id = ?
ORDER BY created_at DESC;
`

	selectRecordSQL = `
SELECT created_at
FROM cache_records
WHERE id = ?
  AND record_id = ?
ORDER BY created_at DESC
LIMIT 1;
`

	queryBacklinksSQL = `
SELECT source_record
		 , input_index
		 , output_index
		 , digest
		 , selector
FROM cache_links
WHERE target_record = ?
ORDER BY source_record ASC;
`

	queryAlternativeRootsSQL = `
SELECT id
FROM cache_records
WHERE id != ? AND record_id = ?
ORDER BY id ASC;
`

	queryDistinctRecordsSQL = `
SELECT DISTINCT record_id
FROM cache_records
ORDER BY record_id ASC;
`

	deleteUnreferencedLinksSQL = `
WITH
	live_records (id) AS (
		SELECT id FROM cache_records
		UNION
		SELECT source_record FROM cache_links
	)
DELETE FROM cache_links
WHERE target_record NOT IN live_records
`
)

var (
	deleteRecordsSQL = tmpl("deleteRecordsSQL", `
DELETE FROM cache_records
WHERE record_id IN ({{range .}}{{if ne . 0}},{{end}}?{{end}})
`)
)

type sqlTemplate struct {
	fn func() *template.Template
}

// tmpl generates an sql template from the given text.
// SQL templates are used to generate queries with varied
// argument counts where a static number isn't viable.
//
// The template for the SQL will only have one parameter with
// the count of the number of arguments that will be passed to it.
// The template is never passed the actual contents of the arguments.
// This is to protect against accidental programmer error related to
// SQL injection since arguments should never be used outside of
// prepared statements.
func tmpl(name, text string) *sqlTemplate {
	fn := sync.OnceValue(func() *template.Template {
		return template.Must(template.New(name).Parse(text))
	})
	return &sqlTemplate{fn}
}

func (t *sqlTemplate) Exec(ctx context.Context, db StatementPreparer, args ...any) (sql.Result, error) {
	var sb strings.Builder
	if err := t.fn().Execute(&sb, len(args)); err != nil {
		return nil, err
	}

	stmt, err := db.PrepareContext(ctx, sb.String())
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	return stmt.ExecContext(ctx, args...)
}

type StatementPreparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

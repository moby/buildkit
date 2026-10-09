package sqlitecachestorage

import (
	"sync"

	"github.com/moby/buildkit/util/sql/template"
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
WITH
	all_records (id) AS (
		SELECT source_record FROM cache_links
		UNION
		SELECT id FROM cache_records
	)
SELECT 1 FROM all_records
WHERE id = ?
LIMIT 1;
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
WITH
	root_records (id) AS (
		SELECT id FROM cache_records
		EXCEPT
		SELECT target_record FROM cache_links
	)
SELECT cache_records.id AS id
FROM cache_records
NATURAL JOIN root_records
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

	// createIndexSearchLinksSQL creates an index used by queryLinksSQL
	// to search for source records based on digest and selector.
	//
	// The order of the index here is important with the indices being
	// before the digest and selector since the indices are constants and
	// the digest and selector may have multiple possible options.
	createIndexSearchLinksSQL = `
CREATE INDEX IF NOT EXISTS cache_links_search_links_index
ON cache_links (output_index, input_index, digest, selector)
`
)

var (
	queryLinksSQL = tmpl("queryLinksSQL", `
WITH
	cache_link_candidates (source_record, digest, selector) AS (
		SELECT source_record, digest, selector
		FROM cache_links
		WHERE output_index = {{bind .Output}}
		  AND input_index = {{bind .Input}}
	)
SELECT DISTINCT source_record AS id
FROM cache_link_candidates
WHERE (digest, selector) IN ({{range $i, $d := .Deps}}{{if ne $i 0}},{{end}}({{bind $d.CacheKey.ID}}, {{bind $d.Selector}}){{end}})
ORDER BY id ASC
`)

	deleteRecordsSQL = tmpl("deleteRecordsSQL", `
DELETE FROM cache_records
WHERE record_id IN ({{range $i, $e := .}}{{if ne $i 0}},{{end}}{{bind $e}}{{end}})
`)
)

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
func tmpl(name, text string) func() *template.Template {
	return sync.OnceValue(func() *template.Template {
		return template.Must(template.New(name).Parse(text))
	})
}

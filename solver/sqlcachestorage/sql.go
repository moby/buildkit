package sqlcachestorage

const (
	createResultsTableSQL = `
CREATE TABLE IF NOT EXISTS results (
	id text NOT NULL,
	created_at timestamp,
	worker_ref_id text NOT NULL
);
	`

	createResultsIndexSQL = `
CREATE INDEX IF NOT EXISTS results_index
ON results (id);
	`

	createResultsByRefIndexSQL = `
CREATE INDEX IF NOT EXISTS results_by_ref_index
ON results (worker_ref_id);
	`

	createLinksTableSQL = `
CREATE TABLE IF NOT EXISTS links (
	source_result_id text NOT NULL,
	vertex_input integer DEFAULT 0,
	vertex_output integer DEFAULT 0,
	vertex_digest text NOT NULL,
	vertex_selector text DEFAULT '',
	target_result_id text NOT NULL,
	UNIQUE (source_result_id, vertex_input, vertex_output, vertex_digest, vertex_selector, target_result_id)
);
	`

	createUnreferencedLinksTriggerSQL = `
CREATE TRIGGER IF NOT EXISTS unreferenced_links_sql AFTER DELETE ON results
BEGIN
	DELETE FROM links
	WHERE NOT EXISTS (SELECT 1 FROM results WHERE id = old.id)
	AND (source_result_id = old.id OR target_result_id = old.id);
END
	`

	createLinksIndexSQL = `
CREATE INDEX IF NOT EXISTS links_index
ON links (source_result_id, vertex_input, vertex_output, vertex_digest, vertex_selector, target_result_id);
	`

	createBacklinksIndexSQL = `
CREATE INDEX IF NOT EXISTS backlinks_index
ON links (target_result_id);
	`

	existsSQL = `
SELECT 1 FROM results WHERE id = ? LIMIT 1;
	`

	walkSQL = `
SELECT id FROM results;
	`

	walkResultsSQL = `
SELECT worker_ref_id, created_at FROM results WHERE id = ?;
	`

	loadSQL = `
SELECT worker_ref_id, created_at FROM results
WHERE id = ? AND worker_ref_id = ?
LIMIT 1;
	`

	addResultSQL = `
INSERT INTO results (id, created_at, worker_ref_id)
VALUES(?, ?, ?);
	`

	deleteResultByRefIDSQL = `
DELETE FROM results WHERE worker_ref_id = ?;
	`

	walkIDsByResultSQL = `
SELECT DISTINCT id FROM results WHERE worker_ref_id = ?;
	`

	addLinkSQL = `
INSERT INTO links (source_result_id, vertex_input, vertex_output, vertex_digest, vertex_selector, target_result_id)
VALUES (?, ?, ?, ?, ?, ?);
	`

	walkLinksSQL = `
SELECT target_result_id FROM links
WHERE source_result_id = ? AND vertex_input = ? AND vertex_output = ? AND vertex_digest = ? AND vertex_selector = ?;
	`

	hasLinkSQL = `
SELECT 1 FROM links
WHERE source_result_id = ?
AND vertex_input = ?
AND vertex_output = ?
AND vertex_digest = ?
AND vertex_selector = ?
AND target_result_id = ?
LIMIT 1;
	`

	walkBacklinksSQL = `
SELECT source_result_id, vertex_input, vertex_output, vertex_digest, vertex_selector FROM links WHERE target_result_id = ?;
	`
)

var createSQL = []string{
	createResultsTableSQL,
	createResultsIndexSQL,
	createResultsByRefIndexSQL,
	createLinksTableSQL,
	createUnreferencedLinksTriggerSQL,
	createLinksIndexSQL,
	createBacklinksIndexSQL,
}
